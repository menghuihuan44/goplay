package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"goplay/internal/domain"
	"goplay/internal/mockgame"
)

// runConcurrency starts 2 and then 4 instances concurrently. Each instance has
// its own game, Lua profile, memory directory, metrics registry and policy. The
// suite verifies that pausing one instance via human takeover does not stop or
// contaminate the others.
func runConcurrency(ctx context.Context, cfg evalConfig, baseDir string) (s Suite) {
	s = Suite{ID: "concurrency", Title: "Concurrent instance isolation"}
	start := time.Now()
	defer func() { s.DurationMS = time.Since(start).Milliseconds() }()

	type sizeResult struct {
		Instances           int       `json:"instances"`
		AllActive           int       `json:"all_active"`
		MinActions          int64     `json:"min_actions"`
		ActionCounts        []int64   `json:"action_counts"`
		Progress            []float64 `json:"progress"`
		CrosstalkViolations int       `json:"crosstalk_violations"`
		IsolationViolations int       `json:"isolation_violations"`
		Error               string    `json:"error,omitempty"`
	}
	var results []sizeResult
	overallOK := true

	for _, n := range cfg.concurrencySizes {
		res := sizeResult{Instances: n}
		dir := filepath.Join(baseDir, fmt.Sprintf("concurrency-%d", n))
		hs := make([]*harness, n)
		cleanup := func() {
			for _, h := range hs {
				if h != nil {
					h.Close()
				}
			}
		}
		ok := true
		for i := 0; i < n; i++ {
			h, err := newHarness(harnessConfig{
				ID: fmt.Sprintf("conc-%d-%d", n, i), Seed: int64(101 + i*17),
				MaxEpisodes: 1, TickMS: 10, HumanResumeMS: 120,
				Dir: filepath.Join(dir, fmt.Sprintf("i%d", i)),
			})
			if err != nil {
				res.Error = fmt.Sprintf("instance %d: %v", i, err)
				ok = false
				break
			}
			hs[i] = h
		}
		if !ok {
			cleanup()
			overallOK = false
			results = append(results, res)
			continue
		}
		for _, h := range hs {
			h.Start()
		}
		for i, h := range hs {
			if err := h.waitActions(ctx, 5, 10*time.Second); err != nil {
				res.Error = fmt.Sprintf("instance %d start: %v", i, err)
				ok = false
				break
			}
		}
		if !ok {
			cleanup()
			overallOK = false
			results = append(results, res)
			continue
		}

		// Isolation: unique game, memory instance, and ID per harness.
		games := map[*mockgame.Game]bool{}
		mems := map[any]bool{}
		for i, h := range hs {
			games[h.Game] = true
			mems[h.Mem] = true
			snap := h.Snapshot()
			if snap.ID != h.ID || h.Mem.GameID != h.ID {
				res.IsolationViolations++
			}
			if snap.Status != domain.StatusRunning {
				res.IsolationViolations++
			}
			if i > 0 && h.Game == hs[0].Game {
				res.IsolationViolations++
			}
		}
		if len(games) != n || len(mems) != n {
			res.IsolationViolations++
		}

		// Cross-talk: put instance 0 into human control; the rest must keep
		// running and keep executing actions.
		res.AllActive = 0
		for _, h := range hs {
			if h.Status() == domain.StatusRunning {
				res.AllActive++
			}
		}
		before := make([]int64, n)
		for i, h := range hs {
			before[i] = h.Snapshot().Stats.Actions
		}
		hs[0].Human.Touch()
		if err := waitFor(ctx, 2*time.Second, "instance 0 human_control", func() bool {
			return hs[0].Status() == domain.StatusHuman
		}); err != nil {
			res.Error = err.Error()
			ok = false
		}
		time.Sleep(150 * time.Millisecond)
		for i := 1; i < n && ok; i++ {
			h := hs[i]
			if h.Status() != domain.StatusRunning {
				res.CrosstalkViolations++
			}
			if h.Snapshot().Stats.Actions <= before[i] {
				res.CrosstalkViolations++
			}
		}
		if ok && hs[0].Status() != domain.StatusHuman {
			res.CrosstalkViolations++
		}
		hs[0].Human.Release()
		_ = waitFor(ctx, 2*time.Second, "instance 0 resume", func() bool {
			return hs[0].Status() == domain.StatusRunning
		})

		for i, h := range hs {
			snap := h.Snapshot()
			res.ActionCounts = append(res.ActionCounts, snap.Stats.Actions)
			res.Progress = append(res.Progress, snap.Stats.Progress)
			if snap.Stats.Actions < 5 {
				res.IsolationViolations++
			}
			_ = i
		}
		res.MinActions = res.ActionCounts[0]
		for _, a := range res.ActionCounts[1:] {
			if a < res.MinActions {
				res.MinActions = a
			}
		}
		cleanup()
		if !ok {
			overallOK = false
		}
		results = append(results, res)
	}

	for _, res := range results {
		prefix := fmt.Sprintf("concurrency.%d", res.Instances)
		s.Add(prefix+".min_actions", fmt.Sprintf("%d instances: minimum actions executed", res.Instances), float64(res.MinActions), "count", "")
		s.Check(prefix+".all_active", fmt.Sprintf("%d instances all running before takeover", res.Instances), float64(res.AllActive), "==", float64(res.Instances), "count", "")
		s.Check(prefix+".crosstalk_violations", fmt.Sprintf("%d instances: no cross-talk on human takeover", res.Instances), float64(res.CrosstalkViolations), "==", 0, "count", "")
		s.Check(prefix+".isolation_violations", fmt.Sprintf("%d instances: no shared game/memory/ID", res.Instances), float64(res.IsolationViolations), "==", 0, "count", "")
		s.Check(prefix+".min_actions", fmt.Sprintf("%d instances: every instance keeps acting", res.Instances), float64(res.MinActions), ">=", 5, "count", "")
	}
	if !overallOK {
		s.Error = "one or more concurrency groups failed to start or lost an instance"
	}
	s.Details = map[string]any{"sizes": results}
	s.Notes = append(s.Notes,
		"Each instance uses its own memory directory, Lua state, mockgame.Game, Metrics registry and Policy.",
		"Cross-talk is tested by touching only instance 0 and confirming every other instance still advances actions.",
	)
	s.Finalize()
	return s
}
