package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExpandTargets(t *testing.T) {
	got, err := expandTargets([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{hostTarget(), TargetAndroid, TargetCLI}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("all targets = %v, want %v", got, want)
	}
	got, err = expandTargets([]string{"windows,android,cli,windows", "android"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "windows,android,cli" {
		t.Fatalf("dedupe = %v", got)
	}
	if _, err := expandTargets([]string{"solaris"}); err == nil {
		t.Fatal("unknown target must fail")
	}
}

func TestBuildRejectsUnsafeVersion(t *testing.T) {
	for _, v := range []string{"", "1.0/evil", "1.0 beta"} {
		if _, err := Build(context.Background(), Options{Version: v, SkipTests: true, SkipNative: true, Targets: []string{TargetCLI}}); err == nil {
			t.Fatalf("version %q must be rejected", v)
		}
	}
}

func TestMergeEnvOverridesCaseInsensitively(t *testing.T) {
	base := []string{"PATH=C:\\bin", "HOME=/tmp", "EMPTY="}
	got := mergeEnv(base, map[string]string{"Path": "D:\\bin", "GOOS": "linux"})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Path=D:\\bin") {
		t.Fatalf("case-insensitive override missing: %v", got)
	}
	if strings.Contains(joined, "PATH=C:\\bin") {
		t.Fatalf("old PATH survived: %v", got)
	}
	if !strings.Contains(joined, "GOOS=linux") {
		t.Fatalf("new var missing: %v", got)
	}
}

func TestPropertiesRoundTrip(t *testing.T) {
	for _, p := range []string{`C:\\Android\\sdk`, `C:/Android/sdk`, `/opt/android-sdk`} {
		enc := escapeProperties(p)
		if strings.Contains(enc, ":") && !strings.Contains(enc, "\\:") {
			t.Fatalf("colon not escaped in %q", enc)
		}
		if got := unescapeProperties(enc); got != p {
			t.Fatalf("roundtrip %q -> %q -> %q", p, enc, got)
		}
	}
}

func TestArchivesContainTopLevelDirectory(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "GoPlay.exe"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(src)
	zipPath := filepath.Join(t.TempDir(), "a.zip")
	if err := zipDir(src, zipPath); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{base + "/GoPlay.exe", base + "/docs/README.md"} {
		if !names[want] {
			t.Fatalf("zip missing %s in %v", want, names)
		}
	}
	tarPath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := tarGzDir(src, tarPath); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	tarNames := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		tarNames[h.Name] = true
	}
	for _, want := range []string{base + "/GoPlay.exe", base + "/docs/README.md"} {
		if !tarNames[want] {
			t.Fatalf("tar missing %s in %v", want, tarNames)
		}
	}
}

func TestWriteReleaseFiles(t *testing.T) {
	dir := t.TempDir()
	b := &builder{outDir: dir, report: Report{Version: "1.2.3", Artifacts: []Artifact{
		{Name: "b.zip", Kind: "windows-zip", SHA256: "bb"},
		{Name: "a.apk", Kind: "android-apk", SHA256: "aa"},
	}}}
	if err := b.writeReleaseFiles(); err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sums) != "aa  a.apk\nbb  b.zip\n" {
		t.Fatalf("sums = %q", sums)
	}
	if !b.hasKind("android-apk") || b.hasKind("linux-tar.gz") {
		t.Fatal("hasKind is wrong")
	}
	if runtime.GOOS == "" {
		t.Fatal("unreachable")
	}
}
