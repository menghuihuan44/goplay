package scheduler

import (
	"testing"
	"time"

	"goplay/internal/domain"
)

func domainRules() domain.InstanceRules { return domain.InstanceRules{} }

func TestDirectiveSurvivesBoundaryUntilTaken(t *testing.T) {
	s := New("g1", "Game", domainRules(), nil, nil, nil, "")
	s.Submit(Message{ID: "m1", Text: "按 A 键", Source: "user_text", At: time.Now()})
	res := s.Boundary(BoundaryBeforeDecision, ClassifyContext{InstanceID: "g1", Now: time.Now()})
	if res.Processed != 1 {
		t.Fatalf("processed=%d", res.Processed)
	}
	ds := s.TakeDirectives()
	if len(ds) != 1 {
		t.Fatalf("expected one directive, got %d", len(ds))
	}
	if ds[0].Action.Input == nil || ds[0].Action.Input.Key != "a" {
		t.Fatalf("unexpected action %+v", ds[0].Action)
	}
	if again := s.TakeDirectives(); len(again) != 0 {
		t.Fatalf("directive is one-shot, got %d again", len(again))
	}
}
