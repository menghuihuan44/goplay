package scheduler

import (
	"testing"
	"time"
)

func TestClassifyDirectivePressKey(t *testing.T) {
	cc := ClassifyContext{InstanceID: "g1", Now: time.Now()}
	msg := Message{ID: "m1", InstanceID: "g1", Text: "按 Z 键", Source: "user_text"}
	in := (RuleClassifier{}).Classify(msg, cc)
	if in.Kind != IntentDirective || in.Directive == nil {
		t.Fatalf("expected directive, got kind=%q", in.Kind)
	}
	if in.Directive.Action.Input == nil || in.Directive.Action.Input.Key != "z" {
		t.Fatalf("expected z key, got %+v", in.Directive.Action)
	}
	if in.Directive.Expires.Before(cc.Now) {
		t.Fatal("directive must be short-lived, not already expired")
	}
}

func TestClassifyDirectiveWalkRight(t *testing.T) {
	cc := ClassifyContext{InstanceID: "g1", Now: time.Now()}
	in := (RuleClassifier{}).Classify(Message{ID: "m2", Text: "往右走", Source: "user_text"}, cc)
	if in.Kind != IntentDirective || in.Directive == nil || in.Directive.Action.Input == nil || in.Directive.Action.Input.Key != "right" {
		t.Fatalf("expected right directive, got %+v", in)
	}
}
