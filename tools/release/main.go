// Command release builds BombeCam's downloads for one version: a Windows ZIP
// and Linux tarballs, each with the license files, plus SHA256SUMS.txt.
//
// Run it from the repository root with Go installed:
//
//	go run ./tools/release                          all targets into dist/
//	go run ./tools/release -targets windows-amd64   just the Windows ZIP
//	go run ./tools/release -version 1.0.0 -notes release-notes.md
//
// -version must equal internal/version.Version, so a release tag, the
// archive names and the version the program reports always agree. Archive
// timestamps come from SOURCE_DATE_EPOCH or the last commit, so the same
// source gives the same archives.
//
// The Osaio server key and app ID are built in, obscured, from
// BOMBECAM_SERVER_KEY, BOMBECAM_APP_ID or osaio-setup.txt (go run
// ./tools/setup). Without them it stops, because such downloads would ask
// every user for the missing value on the web page before they could reach
// Osaio; -allow-no-key builds them anyway (CI does, to check that everything
// builds).
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Fever-r/BombeCam/internal/buildkey"
	"github.com/Fever-r/BombeCam/internal/version"
)

type target struct {
	name, goos, goarch, goarm string
}

// targets are the platforms BombeCam publishes downloads for.
var targets = []target{
	{"windows-amd64", "windows", "amd64", ""},
	{"linux-amd64", "linux", "amd64", ""},
	{"linux-arm64", "linux", "arm64", ""},
	{"linux-armv6", "linux", "arm", "6"}, // also runs on armv7 (Raspberry Pi 2 and later)
}

type program struct {
	file, pkg string
	gui       bool // Windows tray program: no console window
}

func programs(goos string) []program {
	if goos == "windows" {
		return []program{
			{"bombecam.exe", "./cmd/bombecam-gateway", true},
			{"bombecam-gateway.exe", "./cmd/bombecam-gateway", false},
		}
	}
	return []program{
		{"bombecam-gateway", "./cmd/bombecam-gateway", false},
		{"bombecam-net", "./cmd/bombecam-net", false},
		{"bombecam-verify", "./cmd/bombecam-verify", false},
		{"bombecam-policy", "./cmd/bombecam-policy", false},
		{"bombecam-certgen", "./cmd/bombecam-certgen", false},
	}
}

// docs go into every archive; the router script into the Linux ones. The
// configuration and troubleshooting pages explain swapping the server key and
// app ID.
var docs = []string{"README.md", "LICENSE", "DISCLAIMER.md", "THIRD_PARTY_NOTICES.md", "CHANGELOG.md",
	"docs/technical/CONFIGURATION.md", "docs/technical/TROUBLESHOOTING.md"}

const routerScript = "router-setup/bombecam-router.sh"

var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// entry is one file inside an archive.
type entry struct {
	name string
	mode int64
	data []byte
}

func main() {
	ver := flag.String("version", version.Version, "version to build (must equal internal/version.Version)")
	out := flag.String("out", "dist", "output folder (must be empty or not exist)")
	only := flag.String("targets", "", "comma-separated targets (default all: "+targetNames()+")")
	notes := flag.String("notes", "", "also write release notes (CHANGELOG section and download help) to this file")
	allowNoKey := flag.Bool("allow-no-key", false, "build even without the Osaio server key and app ID (test builds only)")
	flag.Parse()
	if err := run(*ver, *out, *only, *notes, *allowNoKey); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(ver, out, only, notes string, allowNoKey bool) error {
	if !semver.MatchString(ver) {
		return fmt.Errorf("version %q is not like 1.2.3", ver)
	}
	if ver != version.Version {
		return fmt.Errorf("version %s does not match internal/version (%s): update internal/version/version.go first", ver, version.Version)
	}
	if _, err := os.Stat("go.mod"); err != nil {
		return errors.New("run this from the repository root")
	}
	found, err := buildkey.Find(".", nil)
	if err != nil {
		return err
	}
	if missing := found.Missing(); len(missing) > 0 && !allowNoKey {
		var names []string
		for _, v := range missing {
			names = append(names, v.Label+" ("+v.Env+")")
		}
		return fmt.Errorf("no %s to build in: run go run ./tools/setup or set the variable (or pass -allow-no-key for test builds)", strings.Join(names, " or "))
	}
	selected, err := selectTargets(only)
	if err != nil {
		return err
	}
	if items, err := os.ReadDir(out); err == nil && len(items) > 0 {
		return fmt.Errorf("%s is not empty; remove it first", out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	stamp := buildTime()
	fmt.Printf("BombeCam %s, files dated %s\n", ver, stamp.UTC().Format(time.RFC3339))
	for _, v := range buildkey.Values {
		if from := found.From[v.Name]; from != "" {
			fmt.Printf("%s built in (obscured) from %s\n", v.Label, from)
		} else {
			fmt.Printf("No %s: these downloads are for testing only\n", v.Label)
		}
	}

	var archives []string
	for _, t := range selected {
		name, err := buildTarget(t, ver, out, stamp, buildkey.LDFlags(found))
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		archives = append(archives, name)
	}
	if err := writeChecksums(out, archives); err != nil {
		return err
	}
	if notes != "" {
		text, err := releaseNotes(ver)
		if err != nil {
			return err
		}
		if err := os.WriteFile(notes, []byte(text), 0o644); err != nil {
			return err
		}
		fmt.Println("release notes:", notes)
	}
	return nil
}

func targetNames() string {
	var names []string
	for _, t := range targets {
		names = append(names, t.name)
	}
	return strings.Join(names, ",")
}

func selectTargets(only string) ([]target, error) {
	if only == "" {
		return targets, nil
	}
	var out []target
	for _, want := range strings.Split(only, ",") {
		found := false
		for _, t := range targets {
			if t.name == strings.TrimSpace(want) {
				out = append(out, t)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown target %q (have %s)", want, targetNames())
		}
	}
	return out, nil
}

// buildTime is SOURCE_DATE_EPOCH, else the last commit's time, else now.
func buildTime() time.Time {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	if b, err := exec.Command("git", "log", "-1", "--format=%ct").Output(); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Now().Truncate(time.Second)
}

func buildTarget(t target, ver, out string, stamp time.Time, osaioFlags string) (string, error) {
	tmp, err := os.MkdirTemp("", "bombecam-release-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	base := fmt.Sprintf("BombeCam-%s-%s", ver, t.name)
	var files []entry
	for _, p := range programs(t.goos) {
		ldflags := "-s -w -buildid="
		if p.gui {
			ldflags += " -H=windowsgui"
		}
		if p.pkg == "./cmd/bombecam-gateway" && osaioFlags != "" {
			ldflags += " " + osaioFlags
		}
		bin := filepath.Join(tmp, p.file)
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", bin, p.pkg)
		cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "GOARM="+t.goarm, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Printf("  build %s/%s %s\n", t.goos, t.goarch, p.file)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("go build %s: %w", p.file, err)
		}
		data, err := os.ReadFile(bin)
		if err != nil {
			return "", err
		}
		files = append(files, entry{base + "/" + p.file, 0o755, data})
	}
	extra := append([]string(nil), docs...)
	if t.goos != "windows" {
		extra = append(extra, routerScript)
	}
	for _, f := range extra {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		mode := int64(0o644)
		if strings.HasSuffix(f, ".sh") {
			mode = 0o755
		}
		files = append(files, entry{base + "/" + filepath.ToSlash(f), mode, data})
	}

	var buf bytes.Buffer
	name := base + ".tar.gz"
	if t.goos == "windows" {
		name = base + ".zip"
		err = writeZip(&buf, files, stamp)
	} else {
		err = writeTarGz(&buf, files, stamp)
	}
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, name), buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	fmt.Printf("  wrote %s (%.1f MB)\n", name, float64(buf.Len())/1e6)
	return name, nil
}

func writeZip(w io.Writer, files []entry, stamp time.Time) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: stamp}
		h.SetMode(os.FileMode(f.mode))
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.data); err != nil {
			return err
		}
	}
	return zw.Close()
}

func writeTarGz(w io.Writer, files []entry, stamp time.Time) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.ModTime = stamp
	tw := tar.NewWriter(gz)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data)), ModTime: stamp,
			Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(f.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeChecksums(out string, names []string) error {
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(out, n))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	fmt.Println("  wrote SHA256SUMS.txt")
	return os.WriteFile(filepath.Join(out, "SHA256SUMS.txt"), []byte(b.String()), 0o644)
}

// releaseNotes is the version's CHANGELOG section plus how to install.
func releaseNotes(ver string) (string, error) {
	f, err := os.Open("CHANGELOG.md")
	if err != nil {
		return "", err
	}
	defer f.Close()
	section, err := changelogSection(f, ver)
	if err != nil {
		return "", err
	}
	return section + fmt.Sprintf(`
## Downloads

- **Windows (64-bit):** BombeCam-%[1]s-windows-amd64.zip. Extract it and run bombecam.exe. Windows may show "Windows protected your PC" for a new download: choose **More info → Run anyway**.
- **Linux:** BombeCam-%[1]s-linux-amd64.tar.gz, -linux-arm64 (64-bit Raspberry Pi and other ARM) or -linux-armv6 (32-bit Raspberry Pi). Extract with tar -xzf and run ./bombecam-gateway -headless.
- **Docker:** ghcr.io/fever-r/bombecam-gateway:%[1]s (amd64, arm64, 32-bit ARM), the Linux program with MediaMTX alongside; deploy/docker-compose.yml in the source runs it. See the README.
- **SHA256SUMS.txt** lists the checksum of each download. On Windows: Get-FileHash .\BombeCam-%[1]s-windows-amd64.zip

If Osaio changes its server key or app ID before the next BombeCam release, paste the new one under Settings → Server key or Settings → App ID (see docs/technical/CONFIGURATION.md in the download). MediaMTX 1.9.3 is downloaded and checksum-verified on first start unless it is already installed. FFmpeg is optional (talk, browser audio and snapshots). See the README for setup.
`, ver), nil
}

// changelogSection returns the lines under "## <ver>" up to the next "## ".
func changelogSection(r io.Reader, ver string) (string, error) {
	var b strings.Builder
	in := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			head := strings.TrimPrefix(line, "## ")
			if head == ver || strings.HasPrefix(head, ver+" ") {
				in = true
			}
			continue
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return "", fmt.Errorf("CHANGELOG.md has no section for %s", ver)
	}
	return text + "\n", nil
}
