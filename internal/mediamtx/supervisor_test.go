package mediamtx

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckPortListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	if !CheckPortListening(port, 500*time.Millisecond) {
		t.Errorf("expected port %d to be listening", port)
	}

	_ = l.Close()

	// Short pause for OS socket release
	time.Sleep(50 * time.Millisecond)

	if CheckPortListening(port, 100*time.Millisecond) {
		t.Errorf("expected closed port %d to NOT be listening", port)
	}
}

func TestSupervisor_AdoptExistingListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	cfg := testConfig(t)
	cfg.RTSPPort = port

	sup := NewSupervisor(cfg)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("supervisor.Start() failed: %v", err)
	}

	if !sup.IsRunning() {
		t.Errorf("expected supervisor to report running")
	}
	if sup.IsManaged() {
		t.Errorf("expected pre-existing listener to be adopted as UNMANAGED, got managed")
	}

	// Stop must leave pre-existing listener intact
	if err := sup.Stop(); err != nil {
		t.Errorf("supervisor.Stop() failed: %v", err)
	}

	if !CheckPortListening(port, 300*time.Millisecond) {
		t.Errorf("unmanaged listener on port %d was unexpectedly terminated", port)
	}
}

func TestSupervisor_BinaryResolution_ExplicitPath(t *testing.T) {
	tmpDir := t.TempDir()
	binName := BinaryName()
	mockBin := filepath.Join(tmpDir, binName)
	if err := os.WriteFile(mockBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create mock binary: %v", err)
	}

	cfg := testConfig(t)
	cfg.BinaryPath = mockBin
	resolved, err := ResolveBinary(cfg)
	if err != nil {
		t.Fatalf("expected resolution to succeed, got %v", err)
	}
	if resolved != mockBin {
		t.Errorf("expected resolved %s, got %s", mockBin, resolved)
	}

	// Test non-existent path
	cfg.BinaryPath = filepath.Join(tmpDir, "nonexistent.exe")
	if _, err := ResolveBinary(cfg); err == nil {
		t.Errorf("expected error for non-existent explicit binary, got nil")
	}
}

func TestSupervisor_BinaryResolution_CacheDir(t *testing.T) {
	t.Setenv("BOMBECAM_MEDIAMTX_PATH", "") // it would win over the cache
	tmpDir := t.TempDir()
	binName := BinaryName()
	cacheBin := filepath.Join(tmpDir, binName)
	if err := os.WriteFile(cacheBin, []byte("binary data"), 0755); err != nil {
		t.Fatalf("failed to write mock cache binary: %v", err)
	}

	cfg := testConfig(t)
	cfg.CacheDir = tmpDir
	cfg.AutoDownload = false

	resolved, err := ResolveBinary(cfg)
	if err != nil {
		t.Fatalf("ResolveBinary from CacheDir failed: %v", err)
	}
	if resolved != cacheBin {
		t.Errorf("expected %s, got %s", cacheBin, resolved)
	}
}

func TestSupervisor_ArchiveExtraction_Zip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	destDir := filepath.Join(tmpDir, "extracted")

	// Create test zip archive
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	binWriter, err := zw.Create("mediamtx.exe")
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	if _, err := binWriter.Write([]byte("mock-exe-content")); err != nil {
		t.Fatalf("failed to write zip content: %v", err)
	}

	cfgWriter, err := zw.Create("mediamtx.yml")
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	if _, err := cfgWriter.Write([]byte("mock-cfg-content")); err != nil {
		t.Fatalf("failed to write zip content: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write zip file: %v", err)
	}

	if err := ExtractZip(zipPath, destDir); err != nil {
		t.Fatalf("ExtractZip failed: %v", err)
	}

	extractedBin := filepath.Join(destDir, "mediamtx.exe")
	if !isExecutableFile(extractedBin) {
		t.Errorf("extracted mediamtx.exe not found or not file")
	}
	extractedCfg := filepath.Join(destDir, "mediamtx.yml")
	if !isExecutableFile(extractedCfg) {
		t.Errorf("extracted mediamtx.yml not found or not file")
	}
}

func TestSupervisor_ArchiveExtraction_TarGz(t *testing.T) {
	tmpDir := t.TempDir()
	tarPath := filepath.Join(tmpDir, "test.tar.gz")
	destDir := filepath.Join(tmpDir, "extracted")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("mock-binary-payload")
	hdr := &tar.Header{
		Name: "mediamtx",
		Mode: 0755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("failed to write tar content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	if err := os.WriteFile(tarPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write tar.gz file: %v", err)
	}

	if err := ExtractTarGz(tarPath, destDir); err != nil {
		t.Fatalf("ExtractTarGz failed: %v", err)
	}

	extractedBin := filepath.Join(destDir, "mediamtx")
	if !isExecutableFile(extractedBin) {
		t.Errorf("extracted mediamtx not found or not file")
	}
}

func TestSupervisor_DownloadAndExtract_MockServer(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	binWriter, err := zw.Create(BinaryName())
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	_, _ = binWriter.Write([]byte("mock-downloaded-binary"))
	_ = zw.Close()
	zipData := buf.Bytes()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer ts.Close()

	sum := sha256.Sum256(zipData)
	destDir := t.TempDir()
	err = DownloadAndExtract(ts.URL+"/mediamtx.zip", hex.EncodeToString(sum[:]), destDir)
	if err != nil {
		t.Fatalf("DownloadAndExtract failed: %v", err)
	}

	targetBin := filepath.Join(destDir, BinaryName())
	if !isExecutableFile(targetBin) {
		t.Errorf("expected %s to exist after download and extract", targetBin)
	}

	// A different archive, or no checksum at all, installs nothing.
	for _, want := range []string{strings.Repeat("0", 64), ""} {
		other := t.TempDir()
		if err := DownloadAndExtract(ts.URL+"/mediamtx.zip", want, other); err == nil {
			t.Errorf("checksum %q: download accepted", want)
		}
		if isExecutableFile(filepath.Join(other, BinaryName())) {
			t.Errorf("checksum %q: binary extracted despite the mismatch", want)
		}
	}
}

// Every platform MediaMTX publishes a build for has a pinned checksum, with
// MediaMTX's own archive names (64-bit ARM Linux is "arm64v8").
func TestDefaultDownloadURLsArePinned(t *testing.T) {
	for _, p := range []struct{ goos, goarch, archive string }{
		{"windows", "amd64", "mediamtx_" + MediaMTXVersion + "_windows_amd64.zip"},
		{"linux", "amd64", "mediamtx_" + MediaMTXVersion + "_linux_amd64.tar.gz"},
		{"linux", "arm64", "mediamtx_" + MediaMTXVersion + "_linux_arm64v8.tar.gz"},
		{"linux", "arm", "mediamtx_" + MediaMTXVersion + "_linux_armv6.tar.gz"},
		{"darwin", "arm64", "mediamtx_" + MediaMTXVersion + "_darwin_arm64.tar.gz"},
	} {
		got := releaseArchive(p.goos, p.goarch)
		if got != p.archive {
			t.Errorf("%s/%s: archive %q, want %q", p.goos, p.goarch, got, p.archive)
		}
		if _, ok := PinnedSHA256("https://github.com/bluenviron/mediamtx/releases/download/" + MediaMTXVersion + "/" + got); !ok {
			t.Errorf("%s/%s: no pinned checksum", p.goos, p.goarch)
		}
	}
	if releaseArchive("plan9", "386") != "" {
		t.Error("an unsupported platform got an archive name")
	}
	if _, ok := PinnedSHA256("https://example.com/mediamtx_v1.9.3_linux_amd64.tar.gz.evil"); ok {
		t.Error("an unknown archive name has a checksum")
	}
}

func TestSupervisor_ActionableError_WhenOfflineAndAbsent(t *testing.T) {
	// No MediaMTX anywhere ResolveBinary looks, even on a machine that has one.
	t.Setenv("BOMBECAM_MEDIAMTX_PATH", "")
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	emptyDir := t.TempDir()
	cfg := testConfig(t)
	cfg.CacheDir = emptyDir
	cfg.AutoDownload = false
	cfg.BinaryPath = ""

	_, err := ResolveBinary(cfg)
	if err == nil {
		t.Fatalf("expected error when binary absent and download disabled, got nil")
	}
	if !strings.Contains(err.Error(), "mediamtx binary not found") {
		t.Errorf("expected actionable error containing 'mediamtx binary not found', got: %v", err)
	}
}

func TestSupervisor_ManagedLifecycle_MockProcess(t *testing.T) {
	// Find a free port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve test port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	time.Sleep(50 * time.Millisecond)

	tmpDir := t.TempDir()
	mockSrc := filepath.Join(tmpDir, "mock_server.go")
	mockBin := filepath.Join(tmpDir, BinaryName())

	src := fmt.Sprintf(`package main

import (
	"net"
	"os"
	"time"
)

func main() {
	l, err := net.Listen("tcp", "127.0.0.1:%d")
	if err != nil {
		os.Exit(1)
	}
	defer l.Close()
	time.Sleep(30 * time.Second)
}
`, port)

	if err := os.WriteFile(mockSrc, []byte(src), 0644); err != nil {
		t.Fatalf("failed to write mock src: %v", err)
	}

	buildCmd := exec.Command("go", "build", "-o", mockBin, mockSrc)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to compile mock server: %v, out: %s", err, string(out))
	}

	cfg := testConfig(t)
	cfg.BinaryPath = mockBin
	cfg.RTSPPort = port
	cfg.StartupTimeout = 5 * time.Second

	sup := NewSupervisor(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	if err := sup.Start(ctx); err != nil {
		t.Fatalf("failed to start supervisor: %v", err)
	}

	if !sup.IsRunning() {
		t.Errorf("expected supervisor to report running")
	}
	if !sup.IsManaged() {
		t.Errorf("expected supervisor to report managed")
	}
	if pid := sup.GetPID(); pid <= 0 {
		t.Errorf("expected valid PID, got %d", pid)
	}

	if !sup.IsListening() {
		t.Errorf("expected port %d to be listening", port)
	}

	// Stop managed subprocess
	if err := sup.Stop(); err != nil {
		t.Errorf("failed to stop supervisor: %v", err)
	}

	// Give OS time to close socket
	time.Sleep(100 * time.Millisecond)

	if sup.IsRunning() {
		t.Errorf("expected supervisor to report not running after stop")
	}
	if CheckPortListening(port, 100*time.Millisecond) {
		t.Errorf("expected port %d to be closed after stop", port)
	}
}

// testConfig is DefaultConfig with the HLS, WebRTC and API ports moved to free
// ports, so the supervisor tests also pass on a machine already running
// BombeCam or MediaMTX.
func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.CacheDir = t.TempDir()
	cfg.LogDir = t.TempDir()
	free := func(network string) int {
		if network == "udp" {
			c, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			return c.LocalAddr().(*net.UDPAddr).Port
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	cfg.HTTPPort, cfg.WebRTCPort, cfg.APIPort = free("tcp"), free("tcp"), free("tcp")
	cfg.WebRTCICEPort = free("udp")
	return cfg
}
