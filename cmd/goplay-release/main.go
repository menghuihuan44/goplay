// Command goplay-release builds downloadable GoPlay artifacts: a Windows
// portable bundle, Android APKs, native host packages, and cross-compiled CLI
// archives. It is the one-command release entry point used locally and by CI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"goplay/internal/release"
	"goplay/internal/version"
)

func main() {
	var (
		targets  = flag.String("target", "all", "comma-separated: all, windows, android, linux, darwin, cli")
		ver      = flag.String("version", "", "release version (default: VERSION file)")
		out      = flag.String("out", "dist", "output directory")
		root     = flag.String("root", ".", "repository root")
		skipTest = flag.Bool("skip-tests", false, "skip go test ./...")
		skipNat  = flag.Bool("skip-native", false, "skip the Windows C++ native build")
		skipAnd  = flag.Bool("skip-android", false, "skip the Android APK build")
		sdk      = flag.String("android-sdk", "", "Android SDK path (default: ANDROID_HOME or local.properties)")
		bundle   = flag.Bool("bundle", false, "also build an Android App Bundle (.aab)")
		clean    = flag.Bool("clean", false, "remove the output directory first")
		showVer  = flag.Bool("version-info", false, "print the release tool version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("goplay-release " + version.String())
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Minute)
	defer cancelTimeout()

	report, err := release.Build(ctx, release.Options{
		Version: *ver, Targets: strings.Split(*targets, ","), OutDir: *out, Root: *root,
		SkipTests: *skipTest, SkipNative: *skipNat, SkipAndroid: *skipAnd,
		AndroidSDK: *sdk, AndroidBundle: *bundle, Clean: *clean,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "goplay-release:", err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(raw))
	fmt.Println("\nArtifacts are in", *out)
}
