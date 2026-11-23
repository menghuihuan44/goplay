package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

type episodeInfo struct {
	Index    int64   `json:"index"`
	Won      bool    `json:"won"`
	Progress float64 `json:"progress"`
	Actions  int64   `json:"actions"`
	Reward   float64 `json:"reward"`
}

type learningSeedResult struct {
	Seed           int64         `json:"seed"`
	Status         string        `json:"status"`
	Episodes       int           `json:"episodes"`
	Wins           int           `json:"wins"`
	WinRate        float64       `json:"win_rate"`
	Reached90      bool          `json:"reached_90"`
	AttemptsTo90   int           `json:"attempts_to_90"`
	BestProgress   float64       `json:"best_progress"`
	Actions        int64         `json:"actions"`
	Errors         int64         `json:"errors"`
	EpisodesDetail []episodeInfo `json:"episodes_detail"`
	Error          string        `json:"error,omitempty"`
}

// runLearning runs the same Policy + Memory instance for MaxEpisodes back to
// back against fresh games, then measures learning convergence: the number of
// attempts needed to reach 90% progress, and win rate.
func runLearning(ctx context.Context, cfg evalConfig, baseDir string) (s Suite) {
	s = Suite{ID: "learning", Title: "Learning convergence across episodes"}
	start := time.Now()
	defer func() { s.DurationMS = time.Since(start).Milliseconds() }()

	var results []learningSeedResult
	totalEpisodes, totalWins := 0, 0
	var totalErrors, totalActions int64
	totalAttempts := 0
	reachedSeeds := 0
	penalty := cfg.learningEpisodes + 1

	for _, seed := range cfg.learningSeeds {
		res := learningSeedResult{Seed: seed}
		dir := filepath.Join(baseDir, fmt.Sprintf("learning-%d", seed))
		h, err := newHarness(harnessConfig{
			ID: fmt.Sprintf("learn-%d", seed), Seed: seed,
			MaxEpisodes: cfg.learningEpisodes, TickMS: 5,
			HumanResumeMS: 150, Dir: dir,
		})
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			s.Error = fmt.Sprintf("seed %d: %v", seed, err)
			continue
		}
		h.Start()
		waitErr := h.waitTerminal(ctx, cfg.learningTimeout)
		snap := h.Snapshot()
		h.Stop()
		h.Close()
		res.Status = string(snap.Status)
		res.Errors = snap.Stats.Errors
		res.BestProgress = snap.Stats.BestProgress
		res.Actions = snap.Stats.Actions
		if waitErr != nil {
			res.Error = waitErr.Error()
		}
		for _, ep := range snap.Episodes {
			res.EpisodesDetail = append(res.EpisodesDetail, episodeInfo{
				Index: ep.Index, Won: ep.Won, Progress: ep.Progress, Actions: ep.Actions, Reward: ep.Reward,
			})
			res.Episodes++
			if ep.Won {
				res.Wins++
			}
			if res.AttemptsTo90 == 0 && ep.Progress >= 0.90 {
				res.AttemptsTo90 = int(ep.Index)
			}
		}
		res.Reached90 = res.AttemptsTo90 > 0
		if res.Episodes > 0 {
			res.WinRate = float64(res.Wins) / float64(res.Episodes)
		}
		totalEpisodes += res.Episodes
		totalWins += res.Wins
		totalErrors += res.Errors
		totalActions += res.Actions
		if res.Reached90 {
			reachedSeeds++
			totalAttempts += res.AttemptsTo90
		} else {
			totalAttempts += penalty
			if res.Error == "" {
				res.Error = "did not reach 90% progress"
			}
		}
		results = append(results, res)
		if waitErr != nil {
			s.Error = fmt.Sprintf("seed %d: %v", seed, waitErr)
		}
		if err := ctx.Err(); err != nil {
			s.Error = err.Error()
			break
		}
	}

	n := len(results)
	winRate, reachedRate, avgAttempts := 0.0, 0.0, float64(penalty)
	if totalEpisodes > 0 {
		winRate = float64(totalWins) / float64(totalEpisodes)
	}
	if n > 0 {
		reachedRate = float64(reachedSeeds) / float64(n)
		avgAttempts = float64(totalAttempts) / float64(n)
	}

	s.Add("learning.episodes_completed", "episodes completed across seeds", float64(totalEpisodes), "count", "")
	s.Add("learning.actions_total", "actions executed across seeds", float64(totalActions), "count", "")
	s.Check("learning.win_rate", "win rate over all episodes", winRate, ">=", 0.50, "ratio", "a win is GroundTruth won at episode end")
	s.Check("learning.reached_90_rate", "fraction of seeds reaching 90% progress", reachedRate, "==", 1.0, "ratio", "at least one episode at >=0.90 progress")
	s.Check("learning.attempts_to_90", "average attempts to reach 90% progress", avgAttempts, "<=", 4.0, "count", fmt.Sprintf("never reached counts as %d; per-seed attempts use Episode.Index", penalty))
	s.Check("learning.errors", "engine errors", float64(totalErrors), "==", 0, "count", fmt.Sprintf("MaxEpisodes=%d, TickMS=5", cfg.learningEpisodes))

	s.Details = map[string]any{
		"configured_episodes": cfg.learningEpisodes,
		"seeds":               results,
	}
	s.Notes = append(s.Notes,
		"Each seed reuses one Policy and one Memory instance for all episodes; Reset() reloads a fresh mock game.",
		"Learning signal is the action reward computed from the built-in mock_dungeon profile plus UCB exploration.",
	)
	s.Finalize()
	return s
}
