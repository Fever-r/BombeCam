package mediamtx

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BinaryName returns the platform-specific executable name ("mediamtx.exe" on Windows, "mediamtx" elsewhere).
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "mediamtx.exe"
	}
	return "mediamtx"
}

// isExecutableFile checks if a file exists and is not a directory.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	return true
}

// ResolveBinary performs multi-stage resolution to find or acquire a viable MediaMTX binary:
// 1. Config / Flag override (cfg.BinaryPath)
// 2. Environment override (BOMBECAM_MEDIAMTX_PATH)
// 3. Executable directory (filepath.Dir(os.Executable()))
// 4. Local cache directory (cfg.CacheDir)
// 5. Working directory and subdirectories (., bin/, deploy/)
// 6. System PATH (exec.LookPath)
// 7. Automated Fetcher (downloads from official release if AutoDownload is enabled)
// 8. Actionable failure if absent
func ResolveBinary(cfg Config) (string, error) {
	binName := BinaryName()

	// 1. Explicit config / flag override
	if cfg.BinaryPath != "" {
		if isExecutableFile(cfg.BinaryPath) {
			abs, err := filepath.Abs(cfg.BinaryPath)
			if err == nil {
				return abs, nil
			}
			return cfg.BinaryPath, nil
		}
		return "", fmt.Errorf("explicit mediamtx binary path not found or not executable: %s", cfg.BinaryPath)
	}

	// 2. Environment variable override
	if envPath := os.Getenv("BOMBECAM_MEDIAMTX_PATH"); envPath != "" {
		if isExecutableFile(envPath) {
			abs, err := filepath.Abs(envPath)
			if err == nil {
				return abs, nil
			}
			return envPath, nil
		}
	}

	// 3. Executable directory (co-located with bombecam.exe)
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidate := filepath.Join(exeDir, binName)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}

	// 4. Local cache directory (%LOCALAPPDATA%\bombecam\bin on Windows, ~/.local/share/... on Linux)
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}
	cacheTarget := filepath.Join(cacheDir, binName)
	if isExecutableFile(cacheTarget) {
		return cacheTarget, nil
	}

	// 5. Working directory candidates (., bin/, bin/<target>/, deploy/)
	candidates := []string{
		binName,
		filepath.Join(".", binName),
		filepath.Join("bin", binName),
		filepath.Join("bin", fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH), binName),
		filepath.Join("deploy", binName),
	}
	for _, cand := range candidates {
		if isExecutableFile(cand) {
			abs, err := filepath.Abs(cand)
			if err == nil {
				return abs, nil
			}
			return cand, nil
		}
	}

	// 6. System PATH lookup
	if pathResult, err := exec.LookPath(binName); err == nil {
		if isExecutableFile(pathResult) {
			abs, err := filepath.Abs(pathResult)
			if err == nil {
				return abs, nil
			}
			return pathResult, nil
		}
	}

	// 7. Automated Fetcher: if enabled, attempt to download and extract release archive
	var downloadErr error
	if cfg.AutoDownload {
		dlURL := cfg.DownloadURL
		if dlURL == "" {
			dlURL = DefaultDownloadURL()
		}
		if sum, known := PinnedSHA256(dlURL); !known {
			downloadErr = fmt.Errorf("no official MediaMTX %s archive with a known checksum for this platform", MediaMTXVersion)
			fmt.Printf("[mediamtx] automated download skipped: %v\n", downloadErr)
		} else {
			fmt.Printf("[mediamtx] binary not found locally; downloading %s (SHA-256 checked)...\n", dlURL)
			if err := DownloadAndExtract(dlURL, sum, cacheDir); err == nil {
				if isExecutableFile(cacheTarget) {
					fmt.Printf("[mediamtx] successfully downloaded and cached mediamtx at %s\n", cacheTarget)
					return cacheTarget, nil
				}
			} else {
				downloadErr = err
				fmt.Printf("[mediamtx] automated download failed: %v\n", err)
			}
		}
	}

	// 8. Graceful actionable failure
	if downloadErr != nil {
		return "", fmt.Errorf("mediamtx binary not found: download failed (%v). Please install mediamtx or place %s into %s, next to the executable, or on PATH",
			downloadErr, binName, cacheDir)
	}

	return "", fmt.Errorf("mediamtx binary not found. Please install mediamtx or place %s into %s, next to the executable, or on PATH",
		binName, cacheDir)
}

// DownloadAndExtract downloads the archive at downloadURL, checks that its
// SHA-256 is wantSHA256 (hex), and extracts the MediaMTX binary into destDir.
// Nothing is extracted from an archive that doesn't match.
func DownloadAndExtract(downloadURL, wantSHA256, destDir string) error {
	if len(wantSHA256) != 64 {
		return fmt.Errorf("refusing to download %s without a SHA-256 checksum", downloadURL)
	}
	// The archive is ~20 MB: allow slow links, but fail fast if the server never answers.
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 20 * time.Second},
	}

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("invalid download URL %s: %w", downloadURL, err)
	}
	req.Header.Set("User-Agent", "bombecam")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP status %d (%s)", resp.StatusCode, resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "mediamtx-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary download file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write downloaded archive: %w", err)
	}
	_ = tmpFile.Close()
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("downloaded archive %s has SHA-256 %s, expected %s: not installed", downloadURL, got, wantSHA256)
	}

	lowerURL := strings.ToLower(downloadURL)
	if strings.HasSuffix(lowerURL, ".zip") {
		return ExtractZip(tmpPath, destDir)
	} else if strings.HasSuffix(lowerURL, ".tar.gz") || strings.HasSuffix(lowerURL, ".tgz") {
		return ExtractTarGz(tmpPath, destDir)
	}

	// If extension is not clear from URL, try zip first then tar.gz
	if err := ExtractZip(tmpPath, destDir); err == nil {
		return nil
	}
	return ExtractTarGz(tmpPath, destDir)
}

// ExtractZip extracts the MediaMTX binary from a zip archive into destDir.
func ExtractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip archive %s: %w", zipPath, err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	found := false
	for _, f := range r.File {
		baseName := filepath.Base(f.Name)
		if baseName == "mediamtx.exe" || baseName == "mediamtx" || baseName == "mediamtx.yml" {
			targetPath := filepath.Join(destDir, baseName)
			if err := extractSingleZipFile(f, targetPath); err != nil {
				return err
			}
			if baseName == "mediamtx.exe" || baseName == "mediamtx" {
				found = true
			}
		}
	}

	if !found {
		return fmt.Errorf("archive did not contain mediamtx executable")
	}
	return nil
}

func extractSingleZipFile(f *zip.File, targetPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, rc); err != nil {
		return err
	}
	return nil
}

// ExtractTarGz extracts the MediaMTX binary from a tar.gz archive into destDir.
func ExtractTarGz(tarGzPath, destDir string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return fmt.Errorf("failed to open tar.gz archive %s: %w", tarGzPath, err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to initialize gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading tar archive: %w", err)
		}

		baseName := filepath.Base(header.Name)
		if baseName == "mediamtx" || baseName == "mediamtx.exe" || baseName == "mediamtx.yml" {
			targetPath := filepath.Join(destDir, baseName)
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("failed to create target file %s: %w", targetPath, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to extract file %s: %w", targetPath, err)
			}
			outFile.Close()
			if baseName == "mediamtx" || baseName == "mediamtx.exe" {
				found = true
			}
		}
	}

	if !found {
		return fmt.Errorf("archive did not contain mediamtx executable")
	}
	return nil
}
