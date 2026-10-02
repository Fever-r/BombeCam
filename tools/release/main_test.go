package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/internal/buildkey"
	"github.com/Fever-r/BombeCam/internal/version"
)

func TestChangelogSection(t *testing.T) {
	log := "# Changelog\n\n## 1.1.0\n\n- newer\n\n## 1.0.0 — first public release\n\n- first\n- second\n\n## 0.1.0\n\n- old\n"
	got, err := changelogSection(strings.NewReader(log), "1.0.0")
	if err != nil || got != "- first\n- second\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := changelogSection(strings.NewReader(log), "2.0.0"); err == nil {
		t.Fatal("a missing version was accepted")
	}
	if _, err := changelogSection(strings.NewReader(log), "1.0"); err == nil {
		t.Fatal("1.0 matched the 1.0.0 section")
	}
}

// Linux programs keep their executable bit, and both archive kinds carry
// the fixed timestamp, so the same source gives the same archives.
func TestArchivesKeepModesAndTime(t *testing.T) {
	stamp := time.Unix(1790000000, 0)
	files := []entry{{"BombeCam-1.0.0-x/bombecam-gateway", 0o755, []byte("bin")}, {"BombeCam-1.0.0-x/LICENSE", 0o644, []byte("text")}}

	var tgz bytes.Buffer
	if err := writeTarGz(&tgz, files, stamp); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&tgz)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for i := 0; ; i++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != files[i].name || h.Mode != files[i].mode || !h.ModTime.Equal(stamp) {
			t.Errorf("tar entry %d: %s %o %v", i, h.Name, h.Mode, h.ModTime)
		}
	}

	var z bytes.Buffer
	if err := writeZip(&z, files, stamp); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(z.Bytes()), int64(z.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range zr.File {
		if f.Name != files[i].name || int64(f.Mode().Perm()) != files[i].mode || !f.Modified.Equal(stamp) {
			t.Errorf("zip entry %d: %s %o %v", i, f.Name, f.Mode().Perm(), f.Modified)
		}
	}
}

func TestRunRefusesAMismatchedVersion(t *testing.T) {
	if err := run("9.9.9", t.TempDir(), "", "", false); err == nil || !strings.Contains(err.Error(), "internal/version") {
		t.Fatalf("got %v", err)
	}
	if err := run("v1", t.TempDir(), "", "", false); err == nil {
		t.Fatal("a malformed version was accepted")
	}
}

// Every user of a release without the Osaio values would have to enter them
// on the web page, so run stops unless told otherwise, and a broken value
// never builds downloads without it.
func TestRunNeedsTheOsaioValues(t *testing.T) {
	t.Chdir("../..")
	if _, err := os.Stat(buildkey.File); err == nil {
		t.Skipf("%s exists in this checkout", buildkey.File)
	}
	t.Setenv(buildkey.ServerKey.Env, "")
	t.Setenv(buildkey.AppID.Env, "")
	if err := run(version.Version, t.TempDir(), "", "", false); err == nil || !strings.Contains(err.Error(), "Osaio server key") || !strings.Contains(err.Error(), "Osaio app ID") {
		t.Fatalf("without the values: %v", err)
	}
	t.Setenv(buildkey.ServerKey.Env, "synthetic-key")
	if err := run(version.Version, t.TempDir(), "", "", false); err == nil || strings.Contains(err.Error(), "server key (") || !strings.Contains(err.Error(), "Osaio app ID") {
		t.Fatalf("without the app ID: %v", err)
	}
	t.Setenv(buildkey.AppID.Env, "has space")
	if err := run(version.Version, t.TempDir(), "", "", true); err == nil || !strings.Contains(err.Error(), "Osaio app ID") {
		t.Fatalf("with a broken app ID: %v", err)
	}
}
