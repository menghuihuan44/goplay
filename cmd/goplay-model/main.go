// Command goplay-model inspects, downloads and serves the small local models
// used by GoPlay. Models are never shipped in the installer: the first run
// recommends a tier from RAM/chip and fetches only that tier's GGUF files.
//
// The local inference server is an external llama.cpp llama-server binary. This
// command does not inject into games, read game memory, hook graphics APIs or
// load kernel drivers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"goplay/internal/config"
	"goplay/internal/localmodel"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "goplay-model: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		rootUsage(stderr)
		return flag.ErrHelp
	}
	switch args[0] {
	case "recommend":
		return cmdRecommend(args[1:], stdout, stderr)
	case "download":
		return cmdDownload(args[1:], stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "bench":
		return cmdBench(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		rootUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want recommend|download|serve|bench)", args[0])
	}
}

func rootUsage(w io.Writer) {
	fmt.Fprint(w, `goplay-model - local GGUF models for GoPlay

Usage:
  goplay-model recommend [flags]   detect hardware and print the tier plan as JSON
  goplay-model download  [flags]   download the selected tier's required models
  goplay-model serve     [flags]   start llama-server and stream NDJSON status
  goplay-model bench     [flags]   measure TTFT/TPS over OpenAI-compatible SSE

Models are NOT bundled with GoPlay. Set GOPLAY_HF_ENDPOINT to use a Hugging
Face mirror. llama-server is also external: set GOPLAY_LLAMA_SERVER or
--runtime. Run a subcommand with -h for its flags.
`)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: goplay-model %s [flags]\n", name)
		fs.PrintDefaults()
	}
	return fs
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func defaultModelsDir() string { return config.Default().Local.ModelsDir }

// resolveRecommendation detects the host, optionally overrides RAM/chip for a
// preview, and applies an explicit --tier preference. Tests use --ram-gb so no
// particular machine is required.
func resolveRecommendation(preferred string, ramGB float64, chip string) (localmodel.Recommendation, error) {
	if ramGB < 0 {
		return localmodel.Recommendation{}, fmt.Errorf("--ram-gb must be positive or zero")
	}
	hw, err := localmodel.Detect()
	if err != nil {
		if ramGB <= 0 {
			return localmodel.Recommendation{}, fmt.Errorf("detect hardware: %w (pass --ram-gb to preview a tier)", err)
		}
		hw = localmodel.HardwareInfo{
			OS:    runtime.GOOS,
			Arch:  runtime.GOARCH,
			Cores: runtime.NumCPU(),
		}
	}
	if ramGB > 0 {
		hw.RAMBytes = uint64(ramGB * float64(localmodel.GiB))
		hw.RAMGB = math.Round(ramGB*100) / 100
	}
	if strings.TrimSpace(chip) != "" {
		hw.ChipModel = strings.TrimSpace(chip)
	}
	hw.Threads = localmodel.RecommendedThreads(hw.Cores)
	return localmodel.Recommend(hw, preferred)
}

type modelDownload struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SizeHuman string `json:"size_human"`
	SHA256    string `json:"sha256"`
	Quant     string `json:"quant"`
	License   string `json:"license"`
}

func modelDownloads(cat localmodel.Catalog, modelsDir string, specs []localmodel.ModelSpec) []modelDownload {
	out := make([]modelDownload, 0, len(specs))
	for _, m := range specs {
		out = append(out, modelDownload{
			ID:        m.ID,
			URL:       cat.URL(m),
			Path:      filepath.Join(modelsDir, m.File),
			SizeBytes: m.SizeBytes,
			SizeHuman: localmodel.HumanBytes(m.SizeBytes),
			SHA256:    m.SHA256,
			Quant:     m.Quant,
			License:   m.License,
		})
	}
	return out
}

type recommendOutput struct {
	Hardware         localmodel.HardwareInfo   `json:"hardware"`
	Recommendation   localmodel.Recommendation `json:"recommendation"`
	Plan             localmodel.TierPlan       `json:"plan"`
	Endpoint         string                    `json:"endpoint"`
	ModelsDir        string                    `json:"models_dir"`
	DownloadRequired []modelDownload           `json:"download_required"`
	Note             string                    `json:"note"`
}

func cmdRecommend(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("recommend", stderr)
	tier := fs.String("tier", "auto", "force tier: auto|low|medium|high")
	ramGB := fs.Float64("ram-gb", 0, "preview with this much RAM instead of detecting (0 = detect)")
	chip := fs.String("chip", "", "preview chip model, e.g. \"Apple M2\"")
	modelsDir := fs.String("models-dir", defaultModelsDir(), "directory that holds GGUF files")
	endpoint := fs.String("endpoint", localmodel.EndpointFromEnv(), "Hugging Face endpoint or mirror")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("recommend: unexpected arguments %q", fs.Args())
	}

	cat := localmodel.NewCatalog(*endpoint)
	rec, err := resolveRecommendation(*tier, *ramGB, *chip)
	if err != nil {
		return err
	}
	plan, ok := cat.Plan(rec.Tier)
	if !ok {
		return fmt.Errorf("catalog has no plan for tier %q", rec.Tier)
	}
	*modelsDir = filepath.Clean(*modelsDir)
	out := recommendOutput{
		Hardware:         rec.Hardware,
		Recommendation:   rec,
		Plan:             plan,
		Endpoint:         cat.Endpoint,
		ModelsDir:        *modelsDir,
		DownloadRequired: modelDownloads(cat, *modelsDir, plan.Models),
		Note: "Models are downloaded on first use, never bundled. Roles run in order: " +
			"perception (rules, optionally a tiny filter model), strategy (one reasoning model), " +
			"validator (deterministic rules, final authority). They are not an ensemble.",
	}
	return writeJSON(stdout, out)
}

type downloadOutput struct {
	Tier      localmodel.Tier             `json:"tier,omitempty"`
	Endpoint  string                      `json:"endpoint"`
	ModelsDir string                      `json:"models_dir"`
	Models    []localmodel.DownloadResult `json:"models"`
}

func cmdDownload(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("download", stderr)
	tierFlag := fs.String("tier", "auto", "tier to download: auto|low|medium|high")
	modelList := fs.String("model", "", "comma-separated model IDs (default: required models for the tier)")
	modelsDir := fs.String("models-dir", defaultModelsDir(), "directory that holds GGUF files")
	endpoint := fs.String("endpoint", localmodel.EndpointFromEnv(), "Hugging Face endpoint or mirror")
	force := fs.Bool("force", false, "re-download even when a valid cached file exists")
	retries := fs.Int("retries", 3, "retries for transient HTTP failures")
	timeout := fs.Duration("timeout", 0, "overall download timeout (0 = no limit)")
	quiet := fs.Bool("quiet", false, "do not print progress to stderr")
	jsonProgress := fs.Bool("json-progress", false, "print progress as JSON lines to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("download: unexpected arguments %q", fs.Args())
	}
	if *retries < 0 {
		return fmt.Errorf("download: --retries must be >= 0")
	}
	if *timeout < 0 {
		return fmt.Errorf("download: --timeout must be >= 0")
	}

	cat := localmodel.NewCatalog(*endpoint)
	ids := parseModelIDs(*modelList)
	var (
		tier  localmodel.Tier
		specs []localmodel.ModelSpec
	)
	if len(ids) > 0 {
		if !strings.EqualFold(strings.TrimSpace(*tierFlag), "auto") {
			t, ok := localmodel.ParseTier(*tierFlag)
			if !ok {
				return fmt.Errorf("download: invalid --tier %q (want auto|low|medium|high)", *tierFlag)
			}
			tier = t
		}
		for _, id := range ids {
			spec, ok := cat.Model(id)
			if !ok {
				return fmt.Errorf("download: unknown model %q", id)
			}
			specs = append(specs, spec)
		}
	} else {
		rec, err := resolveRecommendation(*tierFlag, 0, "")
		if err != nil {
			return err
		}
		tier = rec.Tier
		var ok bool
		specs, ok = cat.RequiredModels(tier)
		if !ok {
			return fmt.Errorf("download: catalog has no plan for tier %q", tier)
		}
	}

	*modelsDir = filepath.Clean(*modelsDir)
	d := localmodel.NewDownloader(*modelsDir, cat.Endpoint)
	d.MaxRetries = *retries
	d.Force = *force
	if !*quiet {
		enc := json.NewEncoder(stderr)
		d.Progress = func(p localmodel.Progress) {
			if *jsonProgress {
				_ = enc.Encode(p)
				return
			}
			if p.Done {
				fmt.Fprintf(stderr, "\r%s: %s  done                    \n", p.ModelID, localmodel.HumanBytes(p.Downloaded))
				return
			}
			total := "?"
			if p.Total > 0 {
				total = localmodel.HumanBytes(p.Total)
			}
			fmt.Fprintf(stderr, "\r%s: %s / %s (%.1f%%) %s/s   ",
				p.ModelID, localmodel.HumanBytes(p.Downloaded), total, p.Percent,
				localmodel.HumanBytes(int64(p.BytesPerSecond)))
		}
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	out := downloadOutput{
		Tier:      tier,
		Endpoint:  cat.Endpoint,
		ModelsDir: *modelsDir,
		Models:    make([]localmodel.DownloadResult, 0, len(specs)),
	}
	for _, spec := range specs {
		res, err := d.Download(ctx, spec)
		if err != nil {
			return fmt.Errorf("download %s (%s): %w", spec.ID, spec.File, err)
		}
		out.Models = append(out.Models, res)
	}
	return writeJSON(stdout, out)
}

func parseModelIDs(v string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(v, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

type serveOutput struct {
	PID       int      `json:"pid"`
	Model     string   `json:"model"`
	Runtime   string   `json:"runtime"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	BaseURL   string   `json:"base_url"`
	HealthURL string   `json:"health_url"`
	Args      []string `json:"args"`
}

type serveStopOutput struct {
	Stopped   bool   `json:"stopped"`
	StopError string `json:"stop_error,omitempty"`
	ExitError string `json:"exit_error,omitempty"`
}

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	modelPath := fs.String("model", "", "GGUF path (default: strategy model for --tier)")
	tierFlag := fs.String("tier", "auto", "tier used when --model is empty")
	modelsDir := fs.String("models-dir", defaultModelsDir(), "directory that holds GGUF files")
	endpoint := fs.String("endpoint", localmodel.EndpointFromEnv(), "Hugging Face endpoint or mirror")
	runtimePath := fs.String("runtime", "", "llama-server path (default: GOPLAY_LLAMA_SERVER or PATH)")
	host := fs.String("host", "127.0.0.1", "loopback host only")
	port := fs.Int("port", config.Default().Local.ServerPort, "loopback port")
	ctxSize := fs.Int("ctx-size", 0, "context tokens (0 = tier/config default)")
	threads := fs.Int("threads", 0, "CPU threads (0 = tier/config default)")
	startupTimeout := fs.Duration("startup-timeout", 120*time.Second, "readiness timeout")
	stopTimeout := fs.Duration("stop-timeout", 10*time.Second, "graceful shutdown timeout")
	var extraArgs []string
	fs.Func("extra-arg", "extra llama-server argument (repeatable, may not override --host)", func(v string) error {
		extraArgs = append(extraArgs, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve: unexpected arguments %q", fs.Args())
	}
	if *startupTimeout <= 0 || *stopTimeout <= 0 {
		return fmt.Errorf("serve: timeouts must be positive")
	}

	cat := localmodel.NewCatalog(*endpoint)
	model := strings.TrimSpace(*modelPath)
	ctxTokens, threadCount := *ctxSize, *threads
	if model == "" {
		rec, err := resolveRecommendation(*tierFlag, 0, "")
		if err != nil {
			return err
		}
		plan, ok := cat.Plan(rec.Tier)
		if !ok {
			return fmt.Errorf("serve: catalog has no plan for tier %q", rec.Tier)
		}
		role, ok := plan.Role(localmodel.RoleStrategy)
		if !ok || role.ModelID == "" {
			return fmt.Errorf("serve: tier %q has no strategy model", rec.Tier)
		}
		spec, ok := cat.Model(role.ModelID)
		if !ok {
			return fmt.Errorf("serve: catalog is missing strategy model %q", role.ModelID)
		}
		model = filepath.Join(*modelsDir, spec.File)
		if _, err := os.Stat(model); err != nil {
			return fmt.Errorf("serve: strategy model for tier %s is not downloaded at %s; run: goplay-model download --tier %s",
				rec.Tier, model, rec.Tier)
		}
		if ctxTokens == 0 {
			ctxTokens = plan.ContextTokens
		}
		if threadCount == 0 {
			threadCount = plan.Threads
		}
	}
	if ctxTokens == 0 {
		ctxTokens = config.Default().Local.ContextTokens
	}
	if threadCount == 0 {
		threadCount = config.Default().Local.Threads
	}
	model = filepath.Clean(model)

	cfg := localmodel.ServerConfig{
		RuntimePath:    *runtimePath,
		ModelPath:      model,
		Host:           *host,
		Port:           *port,
		ContextTokens:  ctxTokens,
		Threads:        threadCount,
		ExtraArgs:      extraArgs,
		StartupTimeout: *startupTimeout,
		StopTimeout:    *stopTimeout,
	}
	srv, err := localmodel.StartServer(context.Background(), cfg)
	if err != nil {
		return err
	}
	stop := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), *stopTimeout)
		defer cancel()
		return srv.Stop(stopCtx)
	}

	start := serveOutput{
		PID:       srv.PID(),
		Model:     srv.ModelPath(),
		Runtime:   srv.RuntimePath(),
		Host:      *host,
		Port:      *port,
		BaseURL:   srv.BaseURL(),
		HealthURL: srv.HealthURL(),
		Args:      srv.Args(),
	}
	if err := writeJSON(stdout, start); err != nil {
		_ = stop()
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		fmt.Fprintln(stderr, "goplay-model: stopping llama-server")
	case <-srv.Exited():
		werr := srv.Wait(context.Background())
		if werr == nil {
			werr = errors.New("llama-server exited with status 0")
		}
		_ = writeJSON(stdout, serveStopOutput{Stopped: false, ExitError: werr.Error()})
		return fmt.Errorf("llama-server exited before shutdown: %w", werr)
	}

	stopErr := stop()
	final := serveStopOutput{Stopped: true}
	if stopErr != nil {
		final.StopError = stopErr.Error()
	}
	_ = writeJSON(stdout, final)
	if stopErr != nil {
		return fmt.Errorf("stop llama-server: %w", stopErr)
	}
	return nil
}

type benchOutput struct {
	Tier               localmodel.Tier         `json:"tier,omitempty"`
	TTFTBudgetMS       int                     `json:"ttft_budget_ms,omitempty"`
	TPSFloor           float64                 `json:"tps_floor,omitempty"`
	Result             localmodel.BenchResult  `json:"result"`
	Checks             []localmodel.BenchCheck `json:"checks,omitempty"`
	OK                 bool                    `json:"ok"`
	Checked            bool                    `json:"checked"`
	TierDetectionError string                  `json:"tier_detection_error,omitempty"`
}

func cmdBench(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("bench", stderr)
	baseURL := fs.String("url", "http://127.0.0.1:8421/v1", "OpenAI-compatible base URL (llama-server /v1)")
	model := fs.String("model", "", "optional model name sent in the request")
	tierFlag := fs.String("tier", "auto", "tier whose TTFT/TPS budget is checked: auto|low|medium|high")
	maxTokens := fs.Int("max-tokens", 32, "completion token cap")
	temperature := fs.Float64("temperature", 0, "sampling temperature")
	timeout := fs.Duration("timeout", 120*time.Second, "request timeout")
	prompt := fs.String("prompt", "", "prompt sent to the model")
	strict := fs.Bool("strict", false, "exit non-zero when a tier budget is missed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("bench: unexpected arguments %q", fs.Args())
	}
	if *maxTokens <= 0 {
		return fmt.Errorf("bench: --max-tokens must be positive")
	}
	if *timeout <= 0 {
		return fmt.Errorf("bench: --timeout must be positive")
	}

	out := benchOutput{}
	cat := localmodel.NewCatalog(localmodel.EndpointFromEnv())
	if !strings.EqualFold(strings.TrimSpace(*tierFlag), "auto") {
		t, ok := localmodel.ParseTier(*tierFlag)
		if !ok {
			return fmt.Errorf("bench: invalid --tier %q (want auto|low|medium|high)", *tierFlag)
		}
		ts, ok := cat.Tier(t)
		if !ok {
			return fmt.Errorf("bench: no catalog tier %q", t)
		}
		out.Tier = ts.ID
		out.TTFTBudgetMS = ts.TTFTBudgetMS
		out.TPSFloor = ts.TPSFloor
		out.Checks = nil
	} else {
		rec, err := resolveRecommendation("auto", 0, "")
		if err != nil {
			out.TierDetectionError = err.Error()
		} else {
			ts, ok := cat.Tier(rec.Tier)
			if ok {
				out.Tier = ts.ID
				out.TTFTBudgetMS = ts.TTFTBudgetMS
				out.TPSFloor = ts.TPSFloor
			}
		}
	}

	res, err := localmodel.Bench(context.Background(), localmodel.BenchConfig{
		BaseURL:        normalizeBaseURL(*baseURL),
		Model:          strings.TrimSpace(*model),
		Prompt:         *prompt,
		MaxTokens:      *maxTokens,
		Temperature:    *temperature,
		RequestTimeout: *timeout,
	})
	if err != nil {
		return err
	}
	out.Result = res
	if out.Tier != "" {
		ts, _ := cat.Tier(out.Tier)
		out.Checks = localmodel.EvaluateTier(ts, res)
		out.Checked = true
		out.OK = localmodel.AllChecksOK(out.Checks)
	} else {
		out.OK = true
	}
	if err := writeJSON(stdout, out); err != nil {
		return err
	}
	if *strict && out.Checked && !out.OK {
		return fmt.Errorf("bench: missed %s tier budget (TTFT %.1fms / %dms, TPS %.1f / %.1f)",
			out.Tier, res.TTFTMS, out.TTFTBudgetMS, res.TPS, out.TPSFloor)
	}
	return nil
}

func normalizeBaseURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	s = strings.TrimSuffix(s, "/chat/completions")
	return s
}
