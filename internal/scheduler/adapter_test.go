package scheduler

import (
	"testing"
	"time"

	"goplay/internal/domain"
	"goplay/internal/memory"
)

func TestAdaptSuppressesConflictAndNeverCreatesHardConstraint(t *testing.T) {
	now := time.Now()
	prefs := []domain.Preference{
		{ID: "p1", Text: "avoid advancing", AvoidTags: []string{"advance"}, Weights: domain.SoftWeights{"speed": 0.7}},
		{ID: "p2", Text: "be careful", Weights: domain.SoftWeights{"risk_aversion": 2}},
	}
	rules := domain.InstanceRules{RequiredTags: []string{"advance"}}
	ctl := Adapt(prefs, rules, now)
	if len(ctl.Hard) != 0 {
		t.Fatalf("preferences became hard constraints: %+v", ctl.Hard)
	}
	if got := ctl.Weights["speed"]; got != 0 {
		t.Fatalf("conflicting preference weight leaked: %v", got)
	}
	if got := ctl.Weights["risk_aversion"]; got != 1 {
		t.Fatalf("soft weight should be clamped to 1, got %v", got)
	}
	if len(ctl.Adaptations) != 2 {
		t.Fatalf("adaptations=%d, want 2", len(ctl.Adaptations))
	}
	if ctl.Adaptations[0].Outcome != "suppressed" && ctl.Adaptations[1].Outcome != "suppressed" {
		t.Fatalf("expected one suppressed adaptation: %+v", ctl.Adaptations)
	}
}

func TestPreferencesAreScopedToTheirGames(t *testing.T) {
	dir := t.TempDir()
	mgr, err := memory.Open(dir, 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddGlobalPreference(domain.Preference{
		ID: "other", Text: "other game only", Weights: domain.SoftWeights{"aggression": 0.5},
		AppliesTo: []string{"Other Game"}, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddGlobalPreference(domain.Preference{
		ID: "this", Text: "this game", Weights: domain.SoftWeights{"aggression": 0.4},
		AppliesTo: []string{"dungeon"}, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	s := New("scope", "Dungeon", domain.InstanceRules{}, mgr, nil, nil, "")
	ctl := s.RefreshControl()
	if got := ctl.Weights["aggression"]; got != 0.4 {
		t.Fatalf("aggression=%v, want only the matching preference (0.4)", got)
	}
	if len(ctl.Hard) != 0 {
		t.Fatalf("unexpected hard constraints: %+v", ctl.Hard)
	}
}

func TestChatConstraintBecomesHardOnlyAtBoundary(t *testing.T) {
	s := New("b1", "Dungeon", domain.InstanceRules{}, nil, nil, nil, "")
	s.Submit(Message{Text: "/constraint 不要攻击", At: time.Now()})
	if got := s.Constraints(); len(got) != 0 {
		t.Fatalf("constraint applied before boundary: %+v", got)
	}
	res := s.Boundary(BoundaryBeforeDecision, ClassifyContext{KnownActions: []string{"attack"}})
	if len(res.Control.Hard) != 1 {
		t.Fatalf("hard constraints=%d, want 1", len(res.Control.Hard))
	}
	if got := s.Pending(); got != 0 {
		t.Fatalf("buffer not drained: %d", got)
	}
	denied, _ := res.Control.Denied(domain.Action{Kind: "attack"}, time.Now())
	if !denied {
		t.Fatal("attack should be denied by the instance constraint")
	}
}
