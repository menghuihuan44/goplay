package engine

import (
	"testing"
	"time"

	"goplay/internal/domain"
)

func TestProbeSeeds(t *testing.T) {
	for seed := int64(7); seed <= 7; seed++ {
		h := newHarnessN(t, "probe", seed, 10)
		h.runner.Start()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			s := h.runner.Status()
			if s == domain.StatusStopped || s == domain.StatusError {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		snap := h.runner.Snapshot()
		gs := h.game.Snapshot()
		t.Logf("seed=%d won=%v dead=%v room=%d hp=%d episodes=%d wins=%d losses=%d actions=%d best=%.2f", seed,
			gs["won"], gs["dead"], gs["room"], gs["hp"],
			snap.Stats.Episodes, snap.Stats.Wins, snap.Stats.Losses, snap.Stats.Actions, snap.Stats.BestProgress)
		h.runner.Stop()
	}
}
