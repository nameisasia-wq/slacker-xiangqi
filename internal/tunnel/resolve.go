package tunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed assets.json
var assetFiles embed.FS

const maxAssetSize = 100 << 20

type Asset struct {
	File          string `json:"file"`
	SHA256        string `json:"sha256"`
	Format        string `json:"format"`
	PackageFile   string `json:"package_file,omitempty"`
	PackageSHA256 string `json:"package_sha256,omitempty"`
}

type Manifest struct {
	Version string           `json:"version"`
	Assets  map[string]Asset `json:"assets"`
}

func OfficialManifest() (Manifest, error) {
	data, err := assetFiles.ReadFile("assets.json")
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func ExecutableName(goos string) string {
	if goos == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

type Resolver struct {
	Binary          string // Explicit override; a bad override is an error.
	ExecutableDir   string // Release package directory.
	CacheDir        string
	LookupPath      func(string) (string, error)
	Client          *http.Client
	DownloadBaseURL string // Used by tests; production defaults to the official release.
	PackageBaseURL  string // Used by tests; production defaults to Cloudflare's package repository.
	Manifest        *Manifest
	Progress        func(downloaded, total int64) // Called while fetching a missing binary.
	Status          func(string)
	IdleTimeout     time.Duration // Maximum time without download data; defaults to one minute.
}

func (r Resolver) Resolve(ctx context.Context) (string, error) {
	return r.resolve(ctx, runtime.GOOS, runtime.GOARCH)
}

func (r Resolver) resolve(ctx context.Context, goos, goarch string) (string, error) {
	if r.Binary != "" {
		if err := validBinary(ctx, r.Binary); err != nil {
			return "", fmt.Errorf("指定的 cloudflared 不可运行：%w", err)
		}
		return r.Binary, nil
	}
	name := ExecutableName(goos)
	if r.ExecutableDir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		r.ExecutableDir = filepath.Dir(exe)
	}
	bundled := filepath.Join(r.ExecutableDir, name)
	if validBinary(ctx, bundled) == nil {
		return bundled, nil
	}
	lookup := r.LookupPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	if installed, err := lookup(name); err == nil && validBinary(ctx, installed) == nil {
		return installed, nil
	}
	m := r.Manifest
	if m == nil {
		official, err := OfficialManifest()
		if err != nil {
			return "", err
		}
		m = &official
	}
	asset, ok := m.Assets[goos+"/"+goarch]
	if !ok {
		return "", fmt.Errorf("暂不支持自动获取 %s/%s 的 cloudflared，请安装后加入 PATH，或用 --tunnel-bin 指定已有程序", goos, goarch)
	}
	if r.CacheDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		r.CacheDir = filepath.Join(cache, "moyu-xiangqi", "cloudflared")
	}
	cached := filepath.Join(r.CacheDir, m.Version, name)
	if validBinary(ctx, cached) == nil {
		return cached, nil
	}
	if err := r.download(ctx, *m, asset, cached); err != nil {
		return "", err
	}
	if err := validBinary(ctx, cached); err != nil {
		_ = os.Remove(cached)
		return "", fmt.Errorf("下载的 cloudflared 无法运行：%w", err)
	}
	return cached, nil
}

func validBinary(parent context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("路径是目录")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "cloudflared") {
		return fmt.Errorf("%s 不是 cloudflared", path)
	}
	return nil
}

func (r Resolver) download(ctx context.Context, manifest Manifest, asset Asset, destination string) error {
	if _, err := hex.DecodeString(asset.SHA256); err != nil || len(asset.SHA256) != 64 {
		return errors.New("无效的 cloudflared 校验值")
	}
	type source struct{ name, url, sha, format string }
	var sources []source
	if asset.PackageFile != "" && asset.PackageSHA256 != "" && (r.DownloadBaseURL == "" || r.PackageBaseURL != "") {
		if _, err := hex.DecodeString(asset.PackageSHA256); err != nil || len(asset.PackageSHA256) != 64 {
			return errors.New("无效的 cloudflared 软件包校验值")
		}
		base := r.PackageBaseURL
		if base == "" {
			base = "https://pkg.cloudflare.com/cloudflared/pool/main/c/cloudflared"
		}
		sources = append(sources, source{"Cloudflare 官方软件包仓库", strings.TrimRight(base, "/") + "/" + asset.PackageFile, asset.PackageSHA256, "deb"})
	}
	base := r.DownloadBaseURL
	if base == "" {
		base = "https://github.com/cloudflare/cloudflared/releases/download/" + manifest.Version
	}
	sources = append(sources, source{"GitHub 官方发布页", strings.TrimRight(base, "/") + "/" + asset.File, asset.SHA256, asset.Format})
	var lastErr error
	for index, source := range sources {
		if r.Status != nil {
			r.Status("正在从" + source.name + "获取 cloudflared...")
		}
		archive, err := r.fetchVerified(ctx, source.url, source.sha, destination)
		if err == nil {
			err = installArchive(ctx, archive, source.format, asset, destination)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		if r.Status != nil && index+1 < len(sources) {
			r.Status(source.name + "获取失败：" + err.Error() + "；尝试备用地址。")
		}
	}
	return lastErr
}

func installArchive(ctx context.Context, archive *os.File, format string, asset Asset, destination string) error {
	defer os.Remove(archive.Name())
	defer archive.Close()
	binary := archive
	if format == "tgz" || format == "deb" {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return err
		}
		var compressed io.Reader = archive
		if format == "deb" {
			var err error
			compressed, err = debData(archive)
			if err != nil {
				return err
			}
		}
		gz, err := gzip.NewReader(compressed)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		extracted, err := os.CreateTemp(filepath.Dir(destination), ".cloudflared-extract-*")
		if err != nil {
			return err
		}
		defer os.Remove(extracted.Name())
		defer extracted.Close()
		found := false
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if filepath.Base(header.Name) != "cloudflared" || header.Typeflag != tar.TypeReg {
				continue
			}
			if header.Size > maxAssetSize {
				return errors.New("cloudflared 解压后过大")
			}
			if _, err := io.Copy(extracted, io.LimitReader(tr, maxAssetSize+1)); err != nil {
				return err
			}
			found = true
			break
		}
		if !found {
			return errors.New("官方压缩包内没有 cloudflared")
		}
		if format == "deb" {
			if _, err := extracted.Seek(0, io.SeekStart); err != nil {
				return err
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, extracted); err != nil {
				return err
			}
			if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), asset.SHA256) {
				return errors.New("软件包内 cloudflared 的 SHA-256 校验失败")
			}
		}
		binary = extracted
	} else if format != "binary" {
		return errors.New("未知的 cloudflared 文件格式")
	}
	if err := binary.Chmod(0700); err != nil {
		return err
	}
	if err := binary.Close(); err != nil {
		return err
	}
	if err := os.Rename(binary.Name(), destination); err != nil {
		// Windows does not replace an existing destination with os.Rename.
		// Another game process may also have populated the cache meanwhile.
		if validBinary(ctx, destination) == nil {
			return nil
		}
		if removeErr := os.Remove(destination); removeErr != nil {
			return fmt.Errorf("替换损坏的 cloudflared 缓存失败：%w", removeErr)
		}
		if err := os.Rename(binary.Name(), destination); err != nil {
			return err
		}
	}
	return nil
}

// debData finds the gzipped payload of a Debian ar archive. The package and
// extracted executable are both checked against pinned SHA-256 digests.
func debData(archive *os.File) (io.Reader, error) {
	info, err := archive.Stat()
	if err != nil {
		return nil, err
	}
	magic := make([]byte, 8)
	if _, err := archive.ReadAt(magic, 0); err != nil || string(magic) != "!<arch>\n" {
		return nil, errors.New("cloudflared 软件包格式无效")
	}
	for offset := int64(8); offset+60 <= info.Size(); {
		header := make([]byte, 60)
		if _, err := archive.ReadAt(header, offset); err != nil {
			return nil, err
		}
		if string(header[58:60]) != "`\n" {
			return nil, errors.New("cloudflared 软件包成员格式无效")
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 || offset+60+size > info.Size() {
			return nil, errors.New("cloudflared 软件包成员大小无效")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[:16])), "/")
		if name == "data.tar.gz" {
			return io.NewSectionReader(archive, offset+60, size), nil
		}
		offset += 60 + size + size%2
	}
	return nil, errors.New("cloudflared 软件包缺少 data.tar.gz")
}

// fetchVerified retains an incomplete archive so a later run can resume it.
// Only a complete archive with the pinned digest can become an executable.
func (r Resolver) fetchVerified(ctx context.Context, url, expectedSHA, destination string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return nil, err
	}
	partial := destination + "." + expectedSHA[:16] + ".download"
	archive, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = archive.Close()
		return nil, err
	}
	discard := func(err error) (*os.File, error) {
		_ = archive.Close()
		_ = os.Remove(partial)
		return nil, err
	}
	info, err := archive.Stat()
	if err != nil {
		return fail(err)
	}
	if info.Size() > maxAssetSize {
		if err := archive.Truncate(0); err != nil {
			return fail(err)
		}
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	hash := sha256.New()
	downloaded, err := io.Copy(hash, archive)
	if err != nil {
		return fail(err)
	}
	if strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA) {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return fail(err)
		}
		return archive, nil
	}
	if _, err := archive.Seek(0, io.SeekEnd); err != nil {
		return fail(err)
	}
	client := r.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 30 * time.Second
		client = &http.Client{Timeout: 2 * time.Hour, Transport: transport}
	}
	idleTimeout := r.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = time.Minute
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	idleTimer := time.AfterFunc(idleTimeout, cancel)
	defer idleTimer.Stop()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return fail(err)
	}
	if downloaded > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
	}
	if r.Progress != nil {
		r.Progress(downloaded, -1)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail(fmt.Errorf("下载 cloudflared 失败（已保留进度，下次可续传）：%w", err))
	}
	defer resp.Body.Close()
	var total int64
	switch resp.StatusCode {
	case http.StatusOK:
		if downloaded > 0 {
			if err := archive.Truncate(0); err != nil {
				return fail(err)
			}
			if _, err := archive.Seek(0, io.SeekStart); err != nil {
				return fail(err)
			}
			downloaded = 0
			hash.Reset()
		}
		total = resp.ContentLength
	case http.StatusPartialContent:
		var start, end int64
		if downloaded == 0 || parseContentRange(resp.Header.Get("Content-Range"), &start, &end, &total) != nil || start != downloaded || end < start || total <= end {
			return discard(errors.New("下载服务器返回了不匹配的续传范围；已清除断点，下次会重新下载"))
		}
	case http.StatusRequestedRangeNotSatisfiable:
		return discard(errors.New("下载服务器不接受当前续传断点；已清除断点，下次会重新下载"))
	default:
		return fail(fmt.Errorf("下载 cloudflared 失败：HTTP %d", resp.StatusCode))
	}
	if total > maxAssetSize {
		return discard(errors.New("cloudflared 下载文件过大"))
	}
	if r.Progress != nil {
		r.Progress(downloaded, total)
	}
	lastReport := downloaded
	lastTime := time.Now()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			idleTimer.Reset(idleTimeout)
			if downloaded+int64(n) > maxAssetSize {
				return discard(errors.New("cloudflared 下载文件过大"))
			}
			if _, err := archive.Write(buffer[:n]); err != nil {
				return fail(err)
			}
			_, _ = hash.Write(buffer[:n])
			downloaded += int64(n)
			if r.Progress != nil && (downloaded-lastReport >= 1<<20 || time.Since(lastTime) >= 15*time.Second || total > 0 && downloaded == total) {
				r.Progress(downloaded, total)
				lastReport, lastTime = downloaded, time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(fmt.Errorf("下载 cloudflared 中断（已保留 %d 字节，下次可续传）：%w", downloaded, readErr))
		}
	}
	if total > 0 && downloaded != total {
		return fail(fmt.Errorf("cloudflared 只下载了 %d/%d 字节；下次运行会续传", downloaded, total))
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, expectedSHA) {
		return discard(errors.New("cloudflared SHA-256 校验失败；已删除损坏的下载文件"))
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return archive, nil
}

func parseContentRange(contentRange string, start, end, total *int64) error {
	n, err := fmt.Sscanf(contentRange, "bytes %d-%d/%d", start, end, total)
	if err == nil && n != 3 {
		return errors.New("续传范围缺少字段")
	}
	return err
}
