package engine

import (
	"testing"
	"time"

	"goplay/internal/domain"
)

func TestResumeClearsHumanImmediately(t *testing.T) {
	h := newHarness(t, "resume-1", 3)
	h.runner.Start()
	defer h.runner.Stop()
	waitFor(t, 10*time.Second, "some actions", func() bool {
		return h.runner.Snapshot().Stats.Actions >= 2
	})
	h.human.Touch()
	waitFor(t, 5*time.Second, "human status", func() bool {
		return h.runner.Status() == domain.StatusHuman
	})
	h.runner.Resume()
	snap := h.runner.Snapshot()
	if snap.Human || snap.Status == domain.StatusHuman {
		t.Fatalf("explicit resume left human control: human=%v status=%s", snap.Human, snap.Status)
	}
}
