package policy

import (
	"context"
	"strings"
	"testing"

	"goplay/internal/domain"
	"goplay/internal/llm"
)

func mockState() domain.State {
	return domain.State{
		Summary: "room 1 hp 100 enemy 20",
		Values:  map[string]float64{"hp": 100, "max_hp": 100, "room": 1, "enemy_hp": 20},
		Text:    map[string]string{"phase": "combat"},
		Tags:    []string{"room1", "combat"},
	}
}

func legalActions() []domain.Action {
	return []domain.Action{
		{Kind: "attack", Target: "enemy", Tags: []string{"combat", "damage"}, Risk: 0.35, Input: &domain.InputSpec{Type: "key", Key: "a"}},
		{Kind: "defend", Target: "enemy", Tags: []string{"combat", "guard"}, Risk: 0.15, Input: &domain.InputSpec{Type: "key", Key: "d"}},
		{Kind: "advance", Target: "next_room", Tags: []string{"advance", "progress"}, Risk: 0.2, Input: &domain.InputSpec{Type: "key", Key: "w"}},
	}
}

func TestConstraintDeniesHard(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	ctl := domain.Control{Hard: []domain.Constraint{{
		ID: "c1", Text: "don't attack", DenyActions: []string{"attack"}, Active: true,
	}}}
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), Control: ctl})
	if d.Action.Kind == "attack" {
		t.Fatalf("constraint should remove attack, got %+v", d.Action)
	}
}

func TestSoftPreferenceNeverFilters(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	ctl := domain.Control{Weights: domain.SoftWeights{"aggression": -1, "risk_aversion": 1}}
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), Control: ctl})
	if len(d.Candidates) != 3 {
		t.Fatalf("soft prefs must not filter candidates, got %d", len(d.Candidates))
	}
	if d.Action.Kind == "attack" {
		t.Fatalf("aggression -1 + risk_aversion 1 should prefer a safer action, got %+v", d.Action)
	}
}

func TestUCBLearns(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	st := mockState()
	rewards := map[string]float64{"attack": 1.0, "defend": 0.0, "advance": 0.1}
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		d := p.Decide(context.Background(), Input{State: st, Actions: legalActions()})
		counts[d.Action.Kind]++
		p.Update(d, rewards[d.Action.Kind])
	}
	if counts["attack"] < 250 {
		t.Fatalf("UCB failed to exploit: %+v", counts)
	}
}

type fixedLLM struct{ reply string }

func (f fixedLLM) Name() string                   { return "fixed" }
func (f fixedLLM) Available(context.Context) bool { return true }
func (f fixedLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: f.reply, Provider: "fixed", Model: "mock"}, nil
}

func TestLLMChoiceValidated(t *testing.T) {
	p := New(fixedLLM{reply: `{"kind":"defend","target":"enemy","confidence":0.9}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if d.Action.Kind != "defend" || d.Source != "llm" {
		t.Fatalf("expected llm defend, got %+v", d)
	}
}

func TestLLMAnnotatedTargetIsGrounded(t *testing.T) {
	// Small local models often repeat an annotated target. The rule layer must
	// map it to the exact legal action rather than rejecting a usable choice.
	p := New(fixedLLM{reply: `{"kind":"attack","target":"enemy 10/28"}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if d.Action.Kind != "attack" || d.Source != "llm" || d.Fallback {
		t.Fatalf("expected grounded llm attack, got %+v", d)
	}
	if d.Action.Target != "enemy" {
		t.Fatalf("target must be the legal canonical target, got %q", d.Action.Target)
	}
}

func TestLLMIllegalFallsBack(t *testing.T) {
	p := New(fixedLLM{reply: `{"kind":"nuke","target":"everything"}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if !d.Fallback || d.Action.Kind == "nuke" {
		t.Fatalf("illegal LLM action must fall back, got %+v", d)
	}
	if !strings.Contains(d.Reason, "illegal") && !strings.Contains(d.Reason, "fallback") {
		t.Fatalf("unexpected reason %q", d.Reason)
	}
}

func TestForcedExplorationAfterIdleStreak(t *testing.T) {
	// An unfamiliar touch game can look alive while the policy keeps choosing
	// wait/back. After two passive decisions it must try a real interaction.
	p := New(nil, nil, DefaultConfig())
	st := mockState()
	wait := domain.Action{Kind: "wait", Target: "screen", Input: &domain.InputSpec{Type: "wait", DurationMS: 300}}
	tap := domain.Action{Kind: "tap", Target: "center", Risk: 0.1, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}}
	for i := 0; i < 100; i++ {
		p.UpdateKey(StateKey(st), tap.Key(), 0)
	}
	p.idleStreak = 2
	d := p.Decide(context.Background(), Input{State: st, Actions: []domain.Action{wait, tap}})
	if d.Source != "explore" || d.Action.Kind != "tap" {
		t.Fatalf("expected forced exploration tap, got %+v", d)
	}
	if !strings.Contains(d.Reason, "强制探索") {
		t.Fatalf("unexpected reason %q", d.Reason)
	}
}

func TestValidatorRejectsBadCoords(t *testing.T) {
	v := NewValidator(0.9)
	legal := []domain.Action{{Kind: "click", Target: "screen", Risk: 0.1, Input: &domain.InputSpec{Type: "click", X: 2, Y: 0.5}}}
	if err := v.Validate(legal[0], legal); err == nil {
		t.Fatal("expected coordinate rejection")
	}
}
