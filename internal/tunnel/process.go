package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var quickURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type StartOptions struct {
	Timeout time.Duration
	Ready   func(context.Context, string, string) error // Tests can supply a local readiness probe.
}

type Process struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	url     string
	urlCh   chan string
	alert   chan error
	exited  chan struct{}
	mu      sync.Mutex
	waitErr error
	closing bool
	logs    []string
}

func Start(parent context.Context, binary, origin, token string, options StartOptions) (*Process, error) {
	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, binary, "tunnel", "--url", origin, "--no-autoupdate")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	p := &Process{cmd: cmd, cancel: cancel, urlCh: make(chan string, 1), alert: make(chan error, 1), exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 cloudflared 失败：%w", err)
	}
	var scanners sync.WaitGroup
	scanners.Add(2)
	for _, stream := range []io.Reader{stdout, stderr} {
		go func(r io.Reader) {
			defer scanners.Done()
			scanner := bufio.NewScanner(r)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			for scanner.Scan() {
				line := scanner.Text()
				p.addLog(line)
				if candidate := quickURL.FindString(line); candidate != "" {
					select {
					case p.urlCh <- candidate:
					default:
					}
				}
			}
		}(stream)
	}
	go func() {
		err := cmd.Wait()
		scanners.Wait()
		p.mu.Lock()
		p.waitErr = err
		closing := p.closing
		p.mu.Unlock()
		if !closing {
			p.alert <- fmt.Errorf("公网隧道已断开，分享地址失效：%s", p.diagnostics(err))
		}
		close(p.exited)
	}()
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 75 * time.Second
	}
	startupCtx, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	select {
	case p.url = <-p.urlCh:
	case <-p.exited:
		p.Close()
		return nil, fmt.Errorf("cloudflared 未生成公网地址：%s", p.diagnostics(p.waitError()))
	case <-startupCtx.Done():
		p.Close()
		return nil, fmt.Errorf("等待 cloudflared 公网地址超时：%s", p.diagnostics(startupCtx.Err()))
	}
	ready := options.Ready
	if ready == nil {
		ready = Probe
	}
	var lastErr error
	for {
		probeCtx, cancelProbe := context.WithTimeout(startupCtx, 12*time.Second)
		lastErr = ready(probeCtx, p.url, token)
		cancelProbe()
		if lastErr == nil {
			select {
			case <-p.exited:
				p.Close()
				return nil, fmt.Errorf("公网隧道刚建立就退出：%s", p.diagnostics(p.waitError()))
			default:
				return p, nil
			}
		}
		select {
		case <-p.exited:
			p.Close()
			return nil, fmt.Errorf("公网隧道启动失败：%s", p.diagnostics(p.waitError()))
		case <-startupCtx.Done():
			p.Close()
			return nil, fmt.Errorf("公网地址尚不可访问：%v；%s", lastErr, p.diagnostics(startupCtx.Err()))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (p *Process) URL() string         { return p.url }
func (p *Process) Alert() <-chan error { return p.alert }

func (p *Process) Close() {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	p.cancel()
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
}

func (p *Process) addLog(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logs = append(p.logs, line)
	if len(p.logs) > 8 {
		p.logs = p.logs[len(p.logs)-8:]
	}
}

func (p *Process) waitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *Process) diagnostics(err error) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		err = errors.New("进程已结束")
	}
	return fmt.Sprintf("%v；日志：%s", err, strings.Join(p.logs, " | "))
}

// Probe verifies that the public URL reaches this game's authenticated state endpoint.
func Probe(ctx context.Context, publicURL, token string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	if err := probeWithClient(ctx, client, publicURL, token); err == nil {
		return nil
	} else {
		// Fresh Quick Tunnel hostnames can be stuck in a local negative DNS cache.
		// Resolve through a public resolver and retain the original TLS hostname.
		fallback, fallbackErr := edgeClient(ctx, publicURL)
		if fallbackErr != nil {
			return err
		}
		if directErr := probeWithClient(ctx, fallback, publicURL, token); directErr == nil {
			return nil
		}
		return err
	}
}

func probeWithClient(ctx context.Context, client *http.Client, publicURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+"/state", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var state struct {
		Rows []string `json:"rows"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&state); err != nil {
		return err
	}
	if len(state.Rows) != 10 {
		return errors.New("公网地址未返回象棋棋盘")
	}
	return nil
}

func edgeClient(ctx context.Context, publicURL string) (*http.Client, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	if !strings.HasSuffix(host, ".trycloudflare.com") {
		return nil, errors.New("不是 Quick Tunnel 地址")
	}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", "1.1.1.1:53")
	}}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("公网 DNS 查询失败：%w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("公网 DNS 查询没有返回地址")
	}
	ip := ips[0].IP.String()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	}}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, nil
}
