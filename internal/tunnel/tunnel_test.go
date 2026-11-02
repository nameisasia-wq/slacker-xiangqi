package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeBinary(t *testing.T, path string) []byte {
	t.Helper()
	data := []byte("#!/bin/sh\necho 'cloudflared version test'\n")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	return data
}

func testManifest(content []byte, format string) Manifest {
	hash := sha256.Sum256(content)
	return Manifest{Version: "test", Assets: map[string]Asset{"linux/amd64": {File: "cloudflared-linux-amd64", SHA256: hex.EncodeToString(hash[:]), Format: format}}}
}

func TestResolverUsesBundledThenInstalled(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "cloudflared")
	fakeBinary(t, bundled)
	installed := filepath.Join(t.TempDir(), "cloudflared")
	fakeBinary(t, installed)
	m := testManifest([]byte("unused"), "binary")
	r := Resolver{ExecutableDir: root, CacheDir: t.TempDir(), Manifest: &m, LookupPath: func(string) (string, error) { return installed, nil }}
	got, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil || got != bundled {
		t.Fatalf("bundle: %q %v", got, err)
	}
	if err := os.Remove(bundled); err != nil {
		t.Fatal(err)
	}
	got, err = r.resolve(context.Background(), "linux", "amd64")
	if err != nil || got != installed {
		t.Fatalf("installed: %q %v", got, err)
	}
	got, err = r.resolve(context.Background(), "linux", "other")
	if err != nil || got != installed {
		t.Fatalf("installed on an unsupported download target: %q %v", got, err)
	}
	r.Binary = installed
	got, err = r.resolve(context.Background(), "other", "other")
	if err != nil || got != installed {
		t.Fatalf("explicit path: %q %v", got, err)
	}
}

func TestOfficialDownloadOptIn(t *testing.T) {
	if os.Getenv("MOYU_TEST_OFFICIAL_DOWNLOAD") != "1" {
		t.Skip("set MOYU_TEST_OFFICIAL_DOWNLOAD=1 for the official download check")
	}
	r := Resolver{ExecutableDir: t.TempDir(), CacheDir: t.TempDir(), LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	first, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Resolve(context.Background())
	if err != nil || first != second {
		t.Fatalf("cache reuse: %q %q %v", first, second, err)
	}
}

func TestResolverDownloadsVerifiesAndCaches(t *testing.T) {
	var hits atomic.Int32
	content := []byte("#!/bin/sh\necho 'cloudflared version test'\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write(content) }))
	defer server.Close()
	m := testManifest(content, "binary")
	r := Resolver{ExecutableDir: t.TempDir(), CacheDir: t.TempDir(), Manifest: &m, DownloadBaseURL: server.URL, LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	cachePath := filepath.Join(r.CacheDir, m.Version, "cloudflared")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("damaged"), 0700); err != nil {
		t.Fatal(err)
	}
	first, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil || first != second || hits.Load() != 1 {
		t.Fatalf("cache: %s %s, hits=%d, err=%v", first, second, hits.Load(), err)
	}
	bad := testManifest([]byte("different"), "binary")
	badResolver := r
	badResolver.CacheDir = t.TempDir()
	badResolver.Manifest = &bad
	if _, err := badResolver.resolve(context.Background(), "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("checksum error: %v", err)
	}
}

func TestResolverResumesInterruptedDownload(t *testing.T) {
	content := append([]byte("#!/bin/sh\necho 'cloudflared version test'\n"), bytes.Repeat([]byte("#padding\n"), 4000)...)
	var requests atomic.Int32
	var resumed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = w.Write(content[:1024])
			w.(http.Flusher).Flush()
			time.Sleep(300 * time.Millisecond)
			return
		}
		var start int64
		if _, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-", &start); err != nil || start < 1024 {
			t.Errorf("missing resume range: %q, %v", request.Header.Get("Range"), err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resumed.Store(start)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start:])
	}))
	defer server.Close()
	m := testManifest(content, "binary")
	cacheDir := t.TempDir()
	r := Resolver{ExecutableDir: t.TempDir(), CacheDir: cacheDir, Manifest: &m, DownloadBaseURL: server.URL, LookupPath: func(string) (string, error) { return "", os.ErrNotExist }, Client: &http.Client{Timeout: 100 * time.Millisecond}}
	if _, err := r.resolve(context.Background(), "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "续传") {
		t.Fatalf("expected saved progress after timeout, got %v", err)
	}
	partial := filepath.Join(cacheDir, m.Version, "cloudflared."+m.Assets["linux/amd64"].SHA256[:16]+".download")
	if info, err := os.Stat(partial); err != nil || info.Size() < 1024 {
		t.Fatalf("partial download missing: %v", err)
	}
	r.Client = server.Client()
	path, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil || resumed.Load() < 1024 {
		t.Fatalf("resume failed: path=%q offset=%d err=%v", path, resumed.Load(), err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file should be removed after success: %v", err)
	}
}

func TestResolverUsesVerifiedOfficialPackage(t *testing.T) {
	binary := []byte("#!/bin/sh\necho 'cloudflared version test'\n")
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./usr/bin/cloudflared", Mode: 0755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	var deb bytes.Buffer
	deb.WriteString("!<arch>\n")
	for _, member := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"data.tar.gz", payload.Bytes()}} {
		fmt.Fprintf(&deb, "%-16s%-12d%-6d%-6d%-8d%-10d`\n", member.name+"/", 0, 0, 0, 644, len(member.data))
		deb.Write(member.data)
		if len(member.data)%2 != 0 {
			deb.WriteByte('\n')
		}
	}
	packageHash := sha256.Sum256(deb.Bytes())
	m := testManifest(binary, "binary")
	asset := m.Assets["linux/amd64"]
	asset.PackageFile = "cloudflared_test_amd64.deb"
	asset.PackageSHA256 = hex.EncodeToString(packageHash[:])
	m.Assets["linux/amd64"] = asset
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.URL.Path != "/cloudflared_test_amd64.deb" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(deb.Bytes())
	}))
	defer server.Close()
	r := Resolver{ExecutableDir: t.TempDir(), CacheDir: t.TempDir(), Manifest: &m, PackageBaseURL: server.URL, LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	path, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil || hits.Load() != 1 {
		t.Fatalf("package download: path=%q hits=%d err=%v", path, hits.Load(), err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("extracted binary mismatch: %v", err)
	}
}

func TestResolverFallsBackToOfficialRelease(t *testing.T) {
	binary := []byte("#!/bin/sh\necho 'cloudflared version test'\n")
	m := testManifest(binary, "binary")
	asset := m.Assets["linux/amd64"]
	asset.PackageFile = "missing.deb"
	asset.PackageSHA256 = asset.SHA256
	m.Assets["linux/amd64"] = asset
	packageServer := httptest.NewServer(http.NotFoundHandler())
	defer packageServer.Close()
	var releaseHits atomic.Int32
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		releaseHits.Add(1)
		_, _ = w.Write(binary)
	}))
	defer releaseServer.Close()
	r := Resolver{
		ExecutableDir: t.TempDir(), CacheDir: t.TempDir(), Manifest: &m,
		PackageBaseURL: packageServer.URL, DownloadBaseURL: releaseServer.URL,
		LookupPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	path, err := r.resolve(context.Background(), "linux", "amd64")
	if err != nil || releaseHits.Load() != 1 {
		t.Fatalf("release fallback: path=%q hits=%d err=%v", path, releaseHits.Load(), err)
	}
}

func TestResolverExtractsOfficialStyleArchive(t *testing.T) {
	content := []byte("#!/bin/sh\necho 'cloudflared version test'\n")
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "cloudflared", Mode: 0700, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(archive.Bytes()) }))
	defer server.Close()
	m := testManifest(archive.Bytes(), "tgz")
	m.Assets["darwin/arm64"] = Asset{File: "cloudflared-darwin-arm64.tgz", SHA256: m.Assets["linux/amd64"].SHA256, Format: "tgz"}
	r := Resolver{ExecutableDir: t.TempDir(), CacheDir: t.TempDir(), Manifest: &m, DownloadBaseURL: server.URL, LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	path, err := r.resolve(context.Background(), "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("extract: %v", err)
	}
}

func TestProcessReadyAndUnexpectedExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'https://fake.trycloudflare.com' >&2\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	p, err := Start(context.Background(), path, "http://127.0.0.1:8765", "token", StartOptions{Timeout: 5 * time.Second, Ready: func(context.Context, string, string) error {
		if attempts.Add(1) < 2 {
			return fmt.Errorf("not ready")
		}
		return nil
	}})
	if err != nil || p.URL() != "https://fake.trycloudflare.com" || attempts.Load() < 2 {
		t.Fatalf("start: %v url=%q attempts=%d", err, p.URL(), attempts.Load())
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case alert := <-p.Alert():
		if alert == nil || !strings.Contains(alert.Error(), "地址失效") {
			t.Fatalf("alert: %v", alert)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing exit alert")
	}
	p.Close()
}

func TestProcessFailsWithoutURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho startup-failed >&2\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), path, "http://127.0.0.1:8765", "token", StartOptions{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "startup-failed") {
		t.Fatalf("startup error: %v", err)
	}
}

func TestProbeRequiresAuthenticatedBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"rows":["0","1","2","3","4","5","6","7","8","9"]}`)
	}))
	defer server.Close()
	if err := Probe(context.Background(), server.URL, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := probeWithClient(context.Background(), server.Client(), server.URL, "wrong"); err == nil {
		t.Fatal("accepted incorrect token")
	}
}
