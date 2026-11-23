// Package release builds reproducible desktop and mobile release artifacts.
// It is intentionally host-oriented: a GUI binary needs the host C toolchain,
// while Android needs an SDK. CI runs the same command on each runner.
package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Targets accepted by Options.Targets.
const (
	TargetWindows = "windows"
	TargetAndroid = "android"
	TargetLinux   = "linux"
	TargetDarwin  = "darwin"
	TargetCLI     = "cli"
	TargetAll     = "all"
)

// Options configures one release build.
type Options struct {
	Version       string
	Targets       []string
	OutDir        string
	Root          string
	SkipTests     bool
	SkipNative    bool
	SkipAndroid   bool
	AndroidSDK    string
	AndroidBundle bool
	Clean         bool
	Log           io.Writer
}

// Artifact is one downloadable file.
type Artifact struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Report is written to dist/release.json.
type Report struct {
	Version   string     `json:"version"`
	Commit    string     `json:"commit"`
	BuildDate string     `json:"build_date"`
	Artifacts []Artifact `json:"artifacts"`
}

type builder struct {
	opts    Options
	version string
	commit  string
	date    string
	log     *log.Logger
	outDir  string
	root    string
	report  Report
	started time.Time
}

// Build runs the requested targets and writes checksums + release.json.
func Build(ctx context.Context, opts Options) (*Report, error) {
	if opts.OutDir == "" {
		opts.OutDir = "dist"
	}
	if opts.Root == "" {
		opts.Root = "."
	}
	if opts.Log == nil {
		opts.Log = os.Stdout
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	out, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = readVersion(root)
	}
	if version == "" {
		return nil, errors.New("release: version is empty (pass -version or create a VERSION file)")
	}
	if strings.ContainsAny(version, " \\/") {
		return nil, fmt.Errorf("release: invalid version %q", version)
	}
	targets, err := expandTargets(opts.Targets)
	if err != nil {
		return nil, err
	}
	if opts.SkipAndroid {
		targets = removeTarget(targets, TargetAndroid)
	}

	b := &builder{
		opts: opts, version: version, commit: gitCommit(root),
		date: time.Now().UTC().Format(time.RFC3339), log: log.New(opts.Log, "", 0),
		outDir: out, root: root, started: time.Now(),
	}
	b.report = Report{Version: version, Commit: b.commit, BuildDate: b.date}
	b.log.Printf("GoPlay release %s (%s)", version, b.commit)
	b.log.Printf("root=%s out=%s targets=%s", root, out, strings.Join(targets, ","))
	if opts.Clean {
		if err := os.RemoveAll(out); err != nil {
			return nil, fmt.Errorf("clean %s: %w", out, err)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	if !opts.SkipTests {
		b.log.Printf("== go test ./... ==")
		if err := b.run(ctx, root, nil, "go", "test", "./...", "-count=1"); err != nil {
			return nil, err
		}
	}
	for _, target := range targets {
		switch target {
		case TargetWindows:
			if err := b.buildWindows(ctx); err != nil {
				return nil, err
			}
		case TargetAndroid:
			if err := b.buildAndroid(ctx); err != nil {
				return nil, err
			}
		case TargetLinux:
			if err := b.buildUnix(ctx, TargetLinux); err != nil {
				return nil, err
			}
		case TargetDarwin:
			if err := b.buildUnix(ctx, TargetDarwin); err != nil {
				return nil, err
			}
		case TargetCLI:
			if err := b.buildCLI(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := b.writeReleaseFiles(); err != nil {
		return nil, err
	}
	b.log.Printf("done in %s: %d artifacts in %s", time.Since(b.started).Round(time.Millisecond), len(b.report.Artifacts), out)
	return &b.report, nil
}

func expandTargets(in []string) ([]string, error) {
	if len(in) == 0 {
		in = []string{TargetAll}
	}
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, raw := range in {
		for _, t := range strings.Split(raw, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			switch t {
			case "":
			case TargetAll:
				add(hostTarget())
				add(TargetAndroid)
				add(TargetCLI)
			case TargetWindows, TargetAndroid, TargetLinux, TargetDarwin, TargetCLI:
				add(t)
			default:
				return nil, fmt.Errorf("release: unknown target %q (want all, windows, android, linux, darwin or cli)", t)
			}
		}
	}
	return out, nil
}

func hostTarget() string {
	switch runtime.GOOS {
	case "windows":
		return TargetWindows
	case "darwin":
		return TargetDarwin
	default:
		return TargetLinux
	}
}

func removeTarget(in []string, target string) []string {
	out := in[:0]
	for _, t := range in {
		if t != target {
			out = append(out, t)
		}
	}
	return out
}

func (b *builder) ldflags(gui bool) string {
	ld := fmt.Sprintf("-s -w -X goplay/internal/version.Version=%s -X goplay/internal/version.Commit=%s -X goplay/internal/version.BuildDate=%s",
		b.version, b.commit, b.date)
	if gui && runtime.GOOS == "windows" {
		ld += " -H windowsgui"
	}
	return ld
}

func (b *builder) buildWindows(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return errors.New("release: target windows must be built on Windows (the desktop shell and native DLL use cgo/Win32)")
	}
	dir := filepath.Join(b.outDir, fmt.Sprintf("GoPlay-%s-windows-amd64", b.version))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b.log.Printf("== windows-amd64 ==")
	if err := b.buildGo(ctx, dir, "GoPlay.exe", "./cmd/goplay-desktop", map[string]string{"CGO_ENABLED": "1", "GOOS": "windows", "GOARCH": "amd64"}, b.ldflags(true)); err != nil {
		return err
	}
	// Windows is case-insensitive: GoPlay.exe and goplay.exe are the same
	// path, so the console client is shipped as goplay-cli.exe.
	cli := map[string]string{
		"goplayd.exe": "./cmd/goplayd", "goplay-cli.exe": "./cmd/goplay",
		"goplay-model.exe": "./cmd/goplay-model", "goplay-eval.exe": "./cmd/goplay-eval",
		"goplay-audit.exe": "./cmd/goplay-audit",
	}
	for name, pkg := range cli {
		if err := b.buildGo(ctx, dir, name, pkg, map[string]string{"CGO_ENABLED": "0", "GOOS": "windows", "GOARCH": "amd64"}, b.ldflags(false)); err != nil {
			return err
		}
	}
	if !b.opts.SkipNative {
		b.log.Printf("-- native capture/input/overlay --")
		if err := b.run(ctx, b.root, nil, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join("native", "build.ps1"), "-Configuration", "Release"); err != nil {
			return fmt.Errorf("native build failed (use -skip-native to package without capture support): %w", err)
		}
		for _, name := range []string{"goplay_native.dll", "goplay-overlay.exe"} {
			if err := copyFile(filepath.Join(b.root, "native", "build", name), filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	if err := b.addDocs(dir); err != nil {
		return err
	}
	zipName := filepath.Base(dir) + ".zip"
	if err := zipDir(dir, filepath.Join(b.outDir, zipName)); err != nil {
		return err
	}
	return b.addArtifact(zipName, "windows-zip", "windows", "amd64")
}

func (b *builder) buildUnix(ctx context.Context, target string) error {
	if runtime.GOOS != target {
		return fmt.Errorf("release: target %s must be built on %s (the GUI uses the host WebView toolkit); use -target cli for cross-compiled daemons", target, target)
	}
	arch := runtime.GOARCH
	dir := filepath.Join(b.outDir, fmt.Sprintf("GoPlay-%s-%s-%s", b.version, target, arch))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b.log.Printf("== %s-%s ==", target, arch)
	if err := b.buildGo(ctx, dir, "goplay-desktop", "./cmd/goplay-desktop", map[string]string{"CGO_ENABLED": "1", "GOOS": target, "GOARCH": arch}, b.ldflags(false)); err != nil {
		return err
	}
	cli := map[string]string{
		"goplayd": "./cmd/goplayd", "goplay": "./cmd/goplay",
		"goplay-model": "./cmd/goplay-model", "goplay-eval": "./cmd/goplay-eval",
		"goplay-audit": "./cmd/goplay-audit",
	}
	for name, pkg := range cli {
		if err := b.buildGo(ctx, dir, name, pkg, map[string]string{"CGO_ENABLED": "0", "GOOS": target, "GOARCH": arch}, b.ldflags(false)); err != nil {
			return err
		}
	}
	if err := b.addDocs(dir); err != nil {
		return err
	}
	archive := filepath.Base(dir) + ".tar.gz"
	if err := tarGzDir(dir, filepath.Join(b.outDir, archive)); err != nil {
		return err
	}
	return b.addArtifact(archive, target+"-tar.gz", target, arch)
}

func (b *builder) buildCLI(ctx context.Context) error {
	platforms := []struct{ os, arch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"},
	}
	for _, p := range platforms {
		// Do not duplicate the host's full package.
		if p.os == runtime.GOOS && (p.os == "windows" || p.os == "linux" || p.os == "darwin") {
			if p.os == "windows" || p.os == runtime.GOOS {
				continue
			}
		}
		dir := filepath.Join(b.outDir, fmt.Sprintf("GoPlay-%s-%s-%s-cli", b.version, p.os, p.arch))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		b.log.Printf("== %s-%s CLI ==", p.os, p.arch)
		ext := ""
		if p.os == "windows" {
			ext = ".exe"
		}
		cli := map[string]string{
			"goplayd" + ext: "./cmd/goplayd", "goplay" + ext: "./cmd/goplay",
			"goplay-model" + ext: "./cmd/goplay-model", "goplay-eval" + ext: "./cmd/goplay-eval",
			"goplay-audit" + ext: "./cmd/goplay-audit",
		}
		for name, pkg := range cli {
			if err := b.buildGo(ctx, dir, name, pkg, map[string]string{"CGO_ENABLED": "0", "GOOS": p.os, "GOARCH": p.arch}, b.ldflags(false)); err != nil {
				return err
			}
		}
		if err := b.addDocs(dir); err != nil {
			return err
		}
		if p.os == "windows" {
			archive := filepath.Base(dir) + ".zip"
			if err := zipDir(dir, filepath.Join(b.outDir, archive)); err != nil {
				return err
			}
			if err := b.addArtifact(archive, "cli-zip", p.os, p.arch); err != nil {
				return err
			}
		} else {
			archive := filepath.Base(dir) + ".tar.gz"
			if err := tarGzDir(dir, filepath.Join(b.outDir, archive)); err != nil {
				return err
			}
			if err := b.addArtifact(archive, "cli-tar.gz", p.os, p.arch); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareAndroidNative builds the Go core for Android/arm64 and stages it with
// the pinned llama-server runtime under jniLibs. The APK build itself only runs
// Gradle, so a clean checkout cannot produce an APK that silently lacks a core.
func (b *builder) prepareAndroidNative(ctx context.Context) error {
	jni := filepath.Join(b.root, "android", "app", "src", "main", "jniLibs", "arm64-v8a")
	if err := os.MkdirAll(jni, 0o755); err != nil {
		return fmt.Errorf("create Android jniLibs: %w", err)
	}
	coreDst := filepath.Join(jni, "libgoplaycore.so")
	b.log.Printf("-- android core (go build, arm64) --")
	env := map[string]string{"CGO_ENABLED": "0", "GOOS": "android", "GOARCH": "arm64"}
	if err := b.run(ctx, b.root, env, "go", "build", "-trimpath", "-buildmode=pie",
		"-ldflags", b.ldflags(false), "-o", coreDst, "./cmd/goplayd"); err != nil {
		return fmt.Errorf("build Android core: %w", err)
	}
	llamaSrc := filepath.Join(b.root, "third_party", "llama.cpp-android", "arm64-v8a", "libllama-server.so")
	llamaDst := filepath.Join(jni, "libllama-server.so")
	if st, err := os.Stat(llamaSrc); err != nil {
		return fmt.Errorf("missing Android llama-server runtime at %s (see third_party/llama.cpp-android/README.goplay.md): %w", llamaSrc, err)
	} else if st.IsDir() {
		return fmt.Errorf("Android llama-server runtime path is a directory: %s", llamaSrc)
	}
	if err := copyFile(llamaSrc, llamaDst); err != nil {
		return fmt.Errorf("stage Android llama-server: %w", err)
	}
	for _, f := range []string{coreDst, llamaDst} {
		st, err := os.Stat(f)
		if err != nil {
			return err
		}
		b.log.Printf("  staged %s (%.1f MiB)", filepath.Base(f), float64(st.Size())/1024/1024)
	}
	return nil
}

func (b *builder) buildAndroid(ctx context.Context) error {
	sdk, err := b.resolveSDK()
	if err != nil {
		return err
	}
	b.log.Printf("== android (sdk=%s) ==", sdk)
	if err := b.prepareAndroidNative(ctx); err != nil {
		return err
	}
	props := filepath.Join(b.root, "android", "local.properties")
	if _, err := os.Stat(props); errors.Is(err, fs.ErrNotExist) {
		line := "sdk.dir=" + escapeProperties(sdk) + "\n"
		if err := os.WriteFile(props, []byte(line), 0o600); err != nil {
			return err
		}
	}
	gradlew := filepath.Join(b.root, "android", "gradlew.bat")
	args := []string{":app:assembleRelease", ":app:assembleDebug"}
	cmdName := gradlew
	if runtime.GOOS != "windows" {
		gradlew = filepath.Join(b.root, "android", "gradlew")
		cmdName = gradlew
		args = []string{":app:assembleRelease", ":app:assembleDebug"}
	}
	if b.opts.AndroidBundle {
		args = append(args, ":app:bundleRelease")
	}
	env := map[string]string{"ANDROID_HOME": sdk, "ANDROID_SDK_ROOT": sdk}
	if err := b.run(ctx, filepath.Join(b.root, "android"), env, cmdName, args...); err != nil {
		return fmt.Errorf("android build failed: %w", err)
	}
	releaseAPK := filepath.Join(b.root, "android", "app", "build", "outputs", "apk", "release", "app-release.apk")
	debugAPK := filepath.Join(b.root, "android", "app", "build", "outputs", "apk", "debug", "app-debug.apk")
	if _, err := os.Stat(releaseAPK); err == nil {
		name := fmt.Sprintf("GoPlay-%s-android.apk", b.version)
		if err := copyFile(releaseAPK, filepath.Join(b.outDir, name)); err != nil {
			return err
		}
		if err := b.addArtifact(name, "android-apk", "android", ""); err != nil {
			return err
		}
	}
	if _, err := os.Stat(debugAPK); err == nil {
		name := fmt.Sprintf("GoPlay-%s-android-debug.apk", b.version)
		if err := copyFile(debugAPK, filepath.Join(b.outDir, name)); err != nil {
			return err
		}
		if err := b.addArtifact(name, "android-apk-debug", "android", ""); err != nil {
			return err
		}
	}
	if !b.hasKind("android-apk") {
		return errors.New("android build produced no release APK; check the Gradle output and signing config")
	}
	if b.opts.AndroidBundle {
		aab := filepath.Join(b.root, "android", "app", "build", "outputs", "bundle", "release", "app-release.aab")
		if _, err := os.Stat(aab); err == nil {
			name := fmt.Sprintf("GoPlay-%s-android.aab", b.version)
			if err := copyFile(aab, filepath.Join(b.outDir, name)); err != nil {
				return err
			}
			if err := b.addArtifact(name, "android-aab", "android", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) resolveSDK() (string, error) {
	candidates := []string{b.opts.AndroidSDK, os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT")}
	if raw, err := os.ReadFile(filepath.Join(b.root, "android", "local.properties")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "sdk.dir=") {
				candidates = append(candidates, unescapeProperties(strings.TrimPrefix(line, "sdk.dir=")))
			}
		}
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, "C:\\Android\\sdk")
	} else {
		home, _ := os.UserHomeDir()
		candidates = append(candidates, filepath.Join(home, "Android", "Sdk"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("release: Android SDK not found; pass -android-sdk or set ANDROID_HOME")
}

func (b *builder) buildGo(ctx context.Context, dir, name, pkg string, env map[string]string, ldflags string) error {
	args := []string{"build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(dir, name)}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, pkg)
	b.log.Printf("  go build %s", name)
	return b.run(ctx, b.root, env, "go", args...)
}

func (b *builder) addDocs(dir string) error {
	files := []string{"README.md", "LICENSE", "config.example.json"}
	for _, f := range files {
		src := filepath.Join(b.root, f)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(dir, f)); err != nil {
				return err
			}
		}
	}
	docs := filepath.Join(b.root, "docs")
	if st, err := os.Stat(docs); err == nil && st.IsDir() {
		if err := copyDir(docs, filepath.Join(dir, "docs")); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) addArtifact(name, kind, osName, arch string) error {
	p := filepath.Join(b.outDir, name)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	sum, err := hashFile(p)
	if err != nil {
		return err
	}
	b.report.Artifacts = append(b.report.Artifacts, Artifact{Name: name, Kind: kind, OS: osName, Arch: arch, Size: st.Size(), SHA256: sum})
	b.log.Printf("  artifact %s (%.1f MiB)", name, float64(st.Size())/1024/1024)
	return nil
}

func (b *builder) hasKind(kind string) bool {
	for _, a := range b.report.Artifacts {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

func (b *builder) writeReleaseFiles() error {
	sort.Slice(b.report.Artifacts, func(i, j int) bool { return b.report.Artifacts[i].Name < b.report.Artifacts[j].Name })
	var sums strings.Builder
	for _, a := range b.report.Artifacts {
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, a.Name)
	}
	if err := os.WriteFile(filepath.Join(b.outDir, "SHA256SUMS.txt"), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b.report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(b.outDir, "release.json"), raw, 0o644)
}

func (b *builder) run(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)
	cmd.Stdout = b.opts.Log
	cmd.Stderr = b.opts.Log
	b.log.Printf("  $ %s %s", name, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	idx := map[string]int{}
	out := append([]string(nil), base...)
	for i, kv := range out {
		if j := strings.IndexByte(kv, '='); j > 0 {
			idx[strings.ToUpper(kv[:j])] = i
		}
	}
	for k, v := range overrides {
		up := strings.ToUpper(k)
		if i, ok := idx[up]; ok {
			out[i] = k + "=" + v
		} else {
			out = append(out, k+"="+v)
			idx[up] = len(out) - 1
		}
	}
	return out
}

func readVersion(root string) string {
	if raw, err := os.ReadFile(filepath.Join(root, "VERSION")); err == nil {
		return strings.TrimSpace(string(raw))
	}
	return ""
}

func gitCommit(root string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func zipDir(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()
	base := filepath.Base(src)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = base + "/" + filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		return err
	})
}

func tarGzDir(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	base := filepath.Base(src)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = base + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
}

// EscapeSDKPath is exported for tests: Java .properties on Windows need
// backslashes and the drive colon escaped.
func escapeProperties(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ":", "\\:")
	return s
}

func unescapeProperties(s string) string {
	s = strings.ReplaceAll(s, "\\:", ":")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}
