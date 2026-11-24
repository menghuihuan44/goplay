package domain

import (
	"math"
	"testing"
)

func fieldByKey(fields []DisplayField, key string) *DisplayField {
	for i := range fields {
		if fields[i].Key == key {
			return &fields[i]
		}
	}
	return nil
}

func TestBuildDisplayDoesNotInventHealth(t *testing.T) {
	st := State{
		Summary: "观察中",
		Values:  map[string]float64{"score": 12, "enemies_alive": 3, "myScore": 7},
		Text:    map[string]string{"phase": "combat"},
		Tags:    []string{"wave2"},
	}
	dv := BuildDisplay(st, InstanceStats{}, "等待", "因为")

	for _, f := range dv.Fields {
		switch f.Key {
		case "hp", "health", "max_hp":
			t.Fatalf("invented health field: %+v", f)
		}
		if f.Label == "生命" || f.Label == "最大生命" {
			t.Fatalf("invented health label: %+v", f)
		}
	}
	if dv.Outcome != nil {
		t.Fatalf("fabricated outcome: %v", *dv.Outcome)
	}
	if dv.Progress != nil {
		t.Fatalf("fabricated progress: %v", *dv.Progress)
	}
	if dv.Summary != "观察中" || dv.LastAction != "等待" || dv.LastReason != "因为" {
		t.Fatalf("snapshot fields lost: %+v", dv)
	}

	score := fieldByKey(dv.Fields, "score")
	if score == nil || score.Label != "分数" || score.Value != "12" || score.Kind != "number" {
		t.Fatalf("score field = %+v", score)
	}
	phase := fieldByKey(dv.Fields, "phase")
	if phase == nil || phase.Value != "combat" || phase.Kind != "status" {
		t.Fatalf("phase field = %+v", phase)
	}
	unknown := fieldByKey(dv.Fields, "enemies_alive")
	if unknown == nil || unknown.Label != "Enemies Alive" {
		t.Fatalf("unknown field label = %+v", unknown)
	}
	if camel := fieldByKey(dv.Fields, "myScore"); camel == nil || camel.Label != "My Score" {
		t.Fatalf("camel field label = %+v", camel)
	}
}

func TestBuildDisplayOutcomeOnlyFromProof(t *testing.T) {
	tests := []struct {
		name  string
		state State
		stats InstanceStats
		want  string
		nil   bool
	}{
		{name: "nothing", state: State{Values: map[string]float64{"score": 1}}, nil: true},
		{name: "win stat", stats: InstanceStats{Wins: 1}, want: "胜"},
		{name: "loss stat", stats: InstanceStats{Losses: 2}, want: "负"},
		{name: "won tag", state: State{Tags: []string{"won"}}, want: "胜"},
		{name: "dead tag", state: State{Tags: []string{"dead"}}, want: "负"},
		{name: "terminal tag", state: State{Tags: []string{"terminal"}}, want: "进行中"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dv := BuildDisplay(tt.state, tt.stats, "", "")
			if tt.nil {
				if dv.Outcome != nil {
					t.Fatalf("outcome = %q, want nil", *dv.Outcome)
				}
				return
			}
			if dv.Outcome == nil || *dv.Outcome != tt.want {
				t.Fatalf("outcome = %v, want %q", dv.Outcome, tt.want)
			}
		})
	}
}

func TestBuildDisplayFieldsAreSortedAndCapped(t *testing.T) {
	values := make(map[string]float64, 30)
	for i := 0; i < 30; i++ {
		key := "k" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		values[key] = float64(i)
	}
	dv := BuildDisplay(State{Values: values}, InstanceStats{}, "", "")
	if len(dv.Fields) != 24 {
		t.Fatalf("field count = %d, want 24", len(dv.Fields))
	}
	for i := 1; i < len(dv.Fields); i++ {
		if dv.Fields[i-1].Key >= dv.Fields[i].Key {
			t.Fatalf("fields not sorted: %q >= %q", dv.Fields[i-1].Key, dv.Fields[i].Key)
		}
	}
	if dv.Fields[0].Key != "ka0" || dv.Fields[23].Key != "kc3" {
		t.Fatalf("unexpected field window: first=%q last=%q", dv.Fields[0].Key, dv.Fields[23].Key)
	}
}

func TestBuildDisplayProgress(t *testing.T) {
	dv := BuildDisplay(State{Values: map[string]float64{"room": 3, "rooms": 10}}, InstanceStats{}, "", "")
	if dv.Progress == nil || math.Abs(*dv.Progress-0.3) > 1e-9 {
		t.Fatalf("room progress = %v, want 0.3", dv.Progress)
	}
	if dv.ProgressLabel != "房间 3/10" {
		t.Fatalf("room progress label = %q", dv.ProgressLabel)
	}

	dv = BuildDisplay(State{Values: map[string]float64{"percent": 45}}, InstanceStats{}, "", "")
	if dv.Progress == nil || math.Abs(*dv.Progress-0.45) > 1e-9 {
		t.Fatalf("percent progress = %v, want 0.45", dv.Progress)
	}
	if dv.ProgressLabel != "进度 45%" {
		t.Fatalf("percent progress label = %q", dv.ProgressLabel)
	}

	dv = BuildDisplay(State{Values: map[string]float64{"hp": 50}}, InstanceStats{}, "", "")
	if dv.Progress != nil {
		t.Fatalf("hp produced progress %v", *dv.Progress)
	}
}
