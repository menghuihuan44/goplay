package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPerceptionSeedAccuracy(t *testing.T) {
	c, err := perceptionSeed(7, 0.001, 40)
	if err != nil {
		t.Fatalf("perceptionSeed: %v", err)
	}
	if c.Samples == 0 {
		t.Fatal("no samples")
	}
	if got := c.ratio(c.Room); got < 0.9 {
		t.Fatalf("room accuracy %.3f, want >= 0.9", got)
	}
	if got := c.ratio(c.HP); got < 0.9 {
		t.Fatalf("hp accuracy %.3f, want >= 0.9", got)
	}
}

func TestReportWrite(t *testing.T) {
	rep := &Report{Version: Version, Mode: "test", GeneratedAt: time.Now(), Environment: envInfo()}
	s := Suite{ID: "demo", Title: "Demo"}
	s.Check("demo.value", "demo value", 1, "==", 1, "count", "")
	s.Finalize()
	rep.Suites = append(rep.Suites, s)
	rep.evaluate()
	if rep.Summary.Verdict != "PASS" {
		t.Fatalf("verdict = %s", rep.Summary.Verdict)
	}
	dir := t.TempDir()
	jp, mp, err := rep.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, p := range []string{jp, mp} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if st.Size() == 0 {
			t.Fatalf("%s is empty", p)
		}
	}
}

func TestMemorySuiteSmall(t *testing.T) {
	cfg := evalConfig{memoryFillers: 900, memoryNoiseEvents: 240}
	s := runMemory(context.Background(), cfg)
	if s.Error != "" {
		t.Fatalf("memory suite error: %s", s.Error)
	}
	if s.Status != "pass" {
		for _, m := range s.Metrics {
			if m.Pass != nil && !*m.Pass {
				t.Logf("failed metric %s = %v", m.ID, m.Value)
			}
		}
		t.Fatalf("memory suite status = %s", s.Status)
	}
}

func TestLearningSuiteOneSeed(t *testing.T) {
	base := t.TempDir()
	cfg := evalConfig{
		learningSeeds: []int64{7}, learningEpisodes: 3,
		learningTimeout: 30 * time.Second,
	}
	s := runLearning(context.Background(), cfg, base)
	if s.Error != "" {
		t.Fatalf("learning suite error: %s", s.Error)
	}
	if s.Status != "pass" {
		for _, m := range s.Metrics {
			if m.Pass != nil && !*m.Pass {
				t.Logf("failed metric %s = %v", m.ID, m.Value)
			}
		}
		t.Fatalf("learning suite status = %s", s.Status)
	}
}

func TestLocalModelSuite(t *testing.T) {
	s := runLocalModel()
	for _, m := range s.Metrics {
		t.Logf("%s = %v %s", m.ID, m.Value, m.Unit)
	}
	if s.Error != "" {
		t.Fatalf("localmodel suite error: %s", s.Error)
	}
	byID := map[string]Metric{}
	for _, m := range s.Metrics {
		byID[m.ID] = m
	}
	if byID["localmodel.tokens"].Value != 8 {
		t.Fatalf("tokens = %v, want 8", byID["localmodel.tokens"].Value)
	}
	if byID["localmodel.text"].Value != 1 {
		t.Fatalf("text match failed: %v", s.Details["got_text"])
	}
	if byID["localmodel.tps"].Value < 20 {
		t.Fatalf("tps = %v, want >= 20", byID["localmodel.tps"].Value)
	}
	t.Logf("ttft = %.1fms", byID["localmodel.stream_ttft_ms"].Value)
}

func TestRunQuickWritesReport(t *testing.T) {
	if testing.Short() {
		t.Skip("quick evaluation takes several seconds")
	}
	dir := filepath.Join(t.TempDir(), "reports")
	rep, err := Run(context.Background(), Options{Quick: true, ReportDir: dir, Timeout: 4 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Suites) != 6 {
		t.Fatalf("suites = %d, want 6", len(rep.Suites))
	}
	if rep.Summary.ChecksTotal == 0 {
		t.Fatal("no threshold checks")
	}
	for _, name := range []string{"eval-report.json", "eval-report.md"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("report %s missing or empty: %v", p, err)
		}
	}
	t.Logf("verdict=%s checks=%d/%d failed=%d", rep.Summary.Verdict, rep.Summary.ChecksPassed, rep.Summary.ChecksTotal, rep.Summary.ChecksFailed)
}
