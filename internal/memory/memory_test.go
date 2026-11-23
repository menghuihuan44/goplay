package memory

import (
	"sync"
	"testing"

	"goplay/internal/domain"
)

func TestInstanceMemoryCompactsAndRecalls(t *testing.T) {
	m := NewInstance("game-a", 32)
	for i := 0; i < 300; i++ {
		m.Add(Entry{
			Kind:       KindDecision,
			Text:       "decision with a distinctive marker " + string(rune('a'+i%26)),
			Tags:       []string{"test", "combat"},
			Importance: 0.4,
		})
	}
	st := m.StatsSnapshot()
	if st.Added != 300 {
		t.Fatalf("added=%d want 300", st.Added)
	}
	if st.Compactions == 0 {
		t.Fatal("expected budget compaction")
	}
	if m.Bytes() > 32*1024 {
		t.Fatalf("memory over budget: %d", m.Bytes())
	}
	res := m.Recall("combat", 5)
	if len(res) == 0 {
		t.Fatal("expected recall results")
	}
	if m.StatsSnapshot().Recalls == 0 {
		t.Fatal("recall counter not updated")
	}
}

func TestInstanceMemoryConcurrentAddAndSnapshot(t *testing.T) {
	m := NewInstance("game-race", 256)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				m.Add(Entry{Kind: KindEvent, Text: "event", Importance: 0.2})
			}
		}(w)
	}
	for i := 0; i < 100; i++ {
		snap := m.SnapshotCopy()
		_ = snap.StatsSnapshot()
		_ = m.StatsSnapshot()
	}
	wg.Wait()
	if got := m.StatsSnapshot().Added; got != 1600 {
		t.Fatalf("added=%d want 1600", got)
	}
}

func TestGlobalMemoryRejectsInstanceState(t *testing.T) {
	g := NewGlobal()
	if err := g.AddFact(Entry{Kind: KindFact, Text: "hp 100", GameID: "game-a"}); err == nil {
		t.Fatal("global memory must reject game-scoped state")
	}
	if err := g.AddFact(Entry{Kind: KindDecision, Text: "attack"}); err == nil {
		t.Fatal("global memory must reject decisions")
	}
	g.UpsertPreference(domain.Preference{Text: "be careful", Weights: map[string]float64{"risk_aversion": 1}})
	if len(g.PreferenceList()) != 1 {
		t.Fatal("preference not stored")
	}
}
