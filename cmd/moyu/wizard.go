package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"moyu-xiangqi/internal/ai"
	"moyu-xiangqi/internal/xiangqi"
)

type setupPrompt struct {
	in     io.Reader
	out    io.Writer
	secret func(string) (string, error)
}

func wizard() error {
	p := setupPrompt{in: os.Stdin, out: os.Stdout, secret: readHiddenLine}
	return runWizard(p)
}

func runWizard(p setupPrompt) error {
	fmt.Fprintln(p.out, "摸鱼象棋")
	mode, err := p.choice("选择模式：1 联机对战，2 本地对局（默认），q 退出 > ", "2", "1", "2")
	if err != nil {
		return err
	}
	langChoice, err := p.choice("棋子显示：1 中文（默认），2 英文 > ", "1", "1", "2")
	if err != nil {
		return err
	}
	lang, langArg := xiangqi.Chinese, "zh"
	if langChoice == "2" {
		lang, langArg = xiangqi.English, "en"
	}
	if mode == "1" {
		role, err := p.choice("联机：1 创建房间（默认），2 加入房间 > ", "1", "1", "2")
		if err != nil {
			return err
		}
		if role == "1" {
			route, err := p.choice("房间地址：1 自动创建公网地址（默认），2 仅本机 > ", "1", "1", "2")
			if err != nil {
				return err
			}
			args := []string{"--lang", langArg}
			if route == "1" {
				args = append(args, "--tunnel")
			}
			return host(args)
		}
		address, err := p.required("房主分享的网址 > ")
		if err != nil {
			return err
		}
		return join([]string{"--lang", langArg, "--url", address})
	}
	localMode, err := p.choice("本地：1 双人轮流下棋（默认），2 人对 AI，3 AI 对 AI > ", "1", "1", "2", "3")
	if err != nil {
		return err
	}
	agents := map[xiangqi.Side]*ai.Agent{}
	if localMode == "2" {
		humanSide, err := p.choice("你执：1 红方（默认），2 黑方 > ", "1", "1", "2")
		if err != nil {
			return err
		}
		aiSide, aiLabel := xiangqi.Black, "黑方"
		if humanSide == "2" {
			aiSide, aiLabel = xiangqi.Red, "红方"
		}
		agents[aiSide], err = p.agent(aiLabel)
		if err != nil {
			return err
		}
	} else if localMode == "3" {
		agents[xiangqi.Red], err = p.agent("红方")
		if err != nil {
			return err
		}
		agents[xiangqi.Black], err = p.agent("黑方")
		if err != nil {
			return err
		}
	}
	explain := false
	if len(agents) > 0 {
		answer, err := p.choice("显示 AI 的简短走棋理由？1 是，2 否（默认，直接出棋） > ", "2", "1", "2")
		if err != nil {
			return err
		}
		explain = answer == "1"
		for _, agent := range agents {
			agent.Explain = explain
		}
	}
	return playSession(lang, agents, explain, 200, 1)
}

func (p setupPrompt) line(label string) (string, error) {
	fmt.Fprint(p.out, label)
	value, err := readLineFrom(p.in)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "q") || strings.EqualFold(value, "quit") {
		return "", errQuit
	}
	return value, nil
}

func (p setupPrompt) required(label string) (string, error) {
	for {
		value, err := p.line(label)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.out, "这里不能为空，请重新输入。")
	}
}

func (p setupPrompt) choice(label, fallback string, options ...string) (string, error) {
	for {
		value, err := p.line(label)
		if err != nil {
			return "", err
		}
		if value == "" {
			value = fallback
		}
		for _, option := range options {
			if value == option {
				return value, nil
			}
		}
		fmt.Fprintln(p.out, "请输入菜单中列出的数字。")
	}
}

func (p setupPrompt) agent(side string) (*ai.Agent, error) {
	fmt.Fprintf(p.out, "配置%s AI（输入 q 可退出）：\n", side)
	var endpoint string
	for {
		raw, err := p.required("Base URL 或完整 /chat/completions、/responses 地址 > ")
		if err != nil {
			return nil, err
		}
		endpoint, err = apiEndpoint(raw)
		if err == nil {
			break
		}
		fmt.Fprintln(p.out, err)
	}
	if p.secret == nil {
		return nil, errors.New("无法安全读取 API Key")
	}
	key, err := p.secret("API Key（输入时不显示） > ")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("API Key 不能为空")
	}
	model, err := p.required("模型 ID（以当前接口服务商的模型列表为准） > ")
	if err != nil {
		return nil, err
	}
	agent := &ai.Agent{Endpoint: endpoint, APIKey: strings.TrimSpace(key), Model: model}
	_, protocol, _ := agent.ThinkingConfig()
	fallback, label := "1", "深度思考：1 关闭（默认），2 开启，3 服务商默认 > "
	if protocol == "" {
		fallback, label = "3", "深度思考：1 关闭，2 开启，3 服务商默认（默认，接口未识别） > "
	}
	answer, err := p.choice(label, fallback, "1", "2", "3")
	if err != nil {
		return nil, err
	}
	agent.Thinking = map[string]ai.ThinkingMode{"1": ai.ThinkingOff, "2": ai.ThinkingOn, "3": ai.ThinkingDefault}[answer]
	if protocol == "" && answer != "3" {
		fmt.Fprintln(p.out, "接口无法自动识别，请按服务商文档选择思考协议；模型必须支持该开关。")
		schema, err := p.choice("思考协议：1 百炼，2 DeepSeek，3 豆包方舟 > ", "1", "1", "2", "3")
		if err != nil {
			return nil, err
		}
		agent.ThinkingAPI = map[string]string{"1": "bailian", "2": "deepseek", "3": "ark"}[schema]
	}
	if _, _, err := agent.ThinkingConfig(); err != nil {
		return nil, err
	}
	fmt.Fprintf(p.out, "AI 配置：接口 %s；深度思考 %s；上下文最近 4 轮 + 整局棋谱；棋理简报已启用；联网工具未启用。\n", agent.ProtocolLabel(), agent.ThinkingLabel())
	return agent, nil
}

func apiEndpoint(raw string) (string, error) { return ai.NormalizeEndpoint(raw) }

func readLineFrom(r io.Reader) (string, error) {
	var b strings.Builder
	var one [1]byte
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			if one[0] == '\r' {
				continue
			}
			if one[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(one[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}

func readHiddenLine(prompt string) (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprint(os.Stdout, prompt)
		return readDirectLine()
	}
	restore, err := terminalInputMode(false)
	if err != nil {
		return "", fmt.Errorf("无法关闭终端回显，请检查终端：%w", err)
	}
	fmt.Fprint(os.Stdout, prompt)
	restoreTerminal := onceRestore(func() { restore(); fmt.Fprintln(os.Stdout) })
	interrupt := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-interrupt:
			restoreTerminal()
			restoreConsole()
			os.Exit(130)
		case <-done:
		}
	}()
	defer func() {
		close(done)
		signal.Stop(interrupt)
		restoreTerminal()
	}()
	return readDirectLine()
}
