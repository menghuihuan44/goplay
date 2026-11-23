// Package eval provides a repeatable, offline evaluation harness for goplay.
package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Options configures a run.
type Options struct {
	Quick     bool          // smaller seeds / episode counts
	ReportDir string        // if empty, no report files are written
	Timeout   time.Duration // overall timeout; 0 = 5 minutes
}

type evalConfig struct {
	mode              string
	perceptionSeeds   []int64
	perceptionSteps   int
	learningSeeds     []int64
	learningEpisodes  int
	learningTimeout   time.Duration
	memoryFillers     int
	memoryNoiseEvents int
	controlSeed       int64
	concurrencySizes  []int
}

func configFor(quick bool) evalConfig {
	if quick {
		return evalConfig{
			mode:              "quick",
			perceptionSeeds:   []int64{3, 7, 11, 19, 23, 29, 31, 37},
			perceptionSteps:   120,
			learningSeeds:     []int64{7, 11, 23},
			learningEpisodes:  8,
			learningTimeout:   45 * time.Second,
			memoryFillers:     900,
			memoryNoiseEvents: 240,
			controlSeed:       7,
			concurrencySizes:  []int{2, 4},
		}
	}
	return evalConfig{
		mode:              "full",
		perceptionSeeds:   []int64{3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53, 59},
		perceptionSteps:   240,
		learningSeeds:     []int64{7, 11, 23, 31, 47, 59},
		learningEpisodes:  12,
		learningTimeout:   120 * time.Second,
		memoryFillers:     1200,
		memoryNoiseEvents: 300,
		controlSeed:       7,
		concurrencySizes:  []int{2, 4},
	}
}

var limitations = []string{
	"Perception is measured on the built-in 320x180 mock renderer, not on a real game window or GPU capture.",
	"Learning convergence uses the built-in mock_dungeon Lua profile and mock game; absolute numbers are not transferable to a commercial game.",
	"Memory scenarios use synthetic entries with unique tokens; Recall@K is exact-token presence, not semantic judgment.",
	"Local-model TTFT/TPS is proven through a local httptest SSE endpoint and llm.Mock; no real local LLM is required or launched.",
	"Timing thresholds are generous and assume an idle host; CI under load can miss latency targets even when behavior is correct.",
	"Safety auditing is a separate package (internal/audit); eval does not replace it.",
	"All tests are offline and do not inject into, read memory of, or hook any real game process.",
}

func safeSuite(ctx context.Context, id, title string, fn func() Suite) (s Suite) {
	defer func() {
		if r := recover(); r != nil {
			s = Suite{ID: id, Title: title, Error: fmt.Sprintf("panic: %v", r)}
			s.Finalize()
		}
	}()
	s = fn()
	if s.ID == "" {
		s.ID = id
	}
	if s.Title == "" {
		s.Title = title
	}
	s.Finalize()
	return s
}

// Run executes all evaluation suites and returns the report. It writes
// eval-report.json and eval-report.md when opts.ReportDir is non-empty.
func Run(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()
	cfg := configFor(opts.Quick)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	baseDir, err := os.MkdirTemp("", "goplay-eval-run-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(baseDir)

	rep := &Report{
		Version:     Version,
		Mode:        cfg.mode,
		GeneratedAt: start,
		Environment: envInfo(),
		Limitations: limitations,
	}

	rep.Suites = append(rep.Suites, safeSuite(runCtx, "perception", "Perception accuracy under renderer noise", func() Suite {
		return runPerception(runCtx, cfg)
	}))
	rep.Suites = append(rep.Suites, safeSuite(runCtx, "learning", "Learning convergence across episodes", func() Suite {
		return runLearning(runCtx, cfg, baseDir)
	}))
	rep.Suites = append(rep.Suites, safeSuite(runCtx, "memory", "Memory recall under a 256KB budget", func() Suite {
		return runMemory(runCtx, cfg)
	}))
	rep.Suites = append(rep.Suites, safeSuite(runCtx, "control", "Human takeover and intervention boundaries", func() Suite {
		return runControl(runCtx, cfg, baseDir)
	}))
	rep.Suites = append(rep.Suites, safeSuite(runCtx, "concurrency", "Concurrent instance isolation", func() Suite {
		return runConcurrency(runCtx, cfg, baseDir)
	}))
	rep.Suites = append(rep.Suites, safeSuite(runCtx, "localmodel", "Local model TTFT/TPS metric chain", func() Suite {
		return runLocalModel()
	}))

	rep.DurationMS = time.Since(start).Milliseconds()
	rep.evaluate()

	if opts.ReportDir != "" {
		jsonPath, mdPath, err := rep.Write(opts.ReportDir)
		if err != nil {
			return rep, fmt.Errorf("write report: %w", err)
		}
		_, _ = jsonPath, mdPath
	}
	return rep, nil
}

// ReportPaths returns the conventional artifact paths for a report directory.
func ReportPaths(dir string) (jsonPath, mdPath string) {
	return filepath.Join(dir, "eval-report.json"), filepath.Join(dir, "eval-report.md")
}
