package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"goplay/internal/localmodel"
)

func TestRecommendPrintsTierPlanJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"recommend",
		"--tier", "low",
		"--ram-gb", "8",
		"--chip", "Test CPU",
		"--models-dir", filepath.Join(t.TempDir(), "models"),
		"--endpoint", "https://hf.example",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run recommend: %v (stderr %s)", err, stderr.String())
	}
	var out recommendOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout.String())
	}
	if out.Plan.Tier != localmodel.TierLow {
		t.Fatalf("tier = %q, want low", out.Plan.Tier)
	}
	if out.Recommendation.Hardware.RAMGB != 8 {
		t.Fatalf("ram_gb = %v, want 8", out.Recommendation.Hardware.RAMGB)
	}
	if out.Recommendation.Hardware.ChipModel != "Test CPU" {
		t.Fatalf("chip = %q", out.Recommendation.Hardware.ChipModel)
	}
	if len(out.Plan.Roles) != 3 {
		t.Fatalf("roles = %d, want 3", len(out.Plan.Roles))
	}
	wantRoles := []localmodel.Role{localmodel.RolePerception, localmodel.RoleStrategy, localmodel.RoleValidator}
	for i, want := range wantRoles {
		if out.Plan.Roles[i].Role != want {
			t.Errorf("role[%d] = %q, want %q", i, out.Plan.Roles[i].Role, want)
		}
	}
	if out.Plan.Roles[2].Mode != localmodel.ModeRules {
		t.Errorf("validator mode = %q, want rules", out.Plan.Roles[2].Mode)
	}
	if len(out.DownloadRequired) != 1 {
		t.Fatalf("low tier downloads = %d, want 1", len(out.DownloadRequired))
	}
	m := out.DownloadRequired[0]
	if m.ID != localmodel.ModelQwen05 {
		t.Errorf("model = %q", m.ID)
	}
	if !strings.HasPrefix(m.URL, "https://hf.example/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/") {
		t.Errorf("url = %q", m.URL)
	}
	if m.SizeBytes != 491400032 || m.Quant != "Q4_K_M" || m.License != "Apache-2.0" {
		t.Errorf("metadata = size %d quant %q license %q", m.SizeBytes, m.Quant, m.License)
	}
	if out.ModelsDir != filepath.Clean(out.ModelsDir) {
		t.Errorf("models dir not cleaned: %q", out.ModelsDir)
	}
	if out.Note == "" || !strings.Contains(out.Note, "not an ensemble") {
		t.Errorf("note missing non-ensemble wording: %q", out.Note)
	}
}

func TestRecommendRejectsBadTier(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"recommend", "--tier", "ultra", "--ram-gb", "8"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "invalid tier") {
		t.Fatalf("err = %v, want invalid tier", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"frobnicate"}, &stdout, &stderr); err == nil {
		t.Fatal("expected unknown command error")
	}
}

func TestParseModelIDsAndNormalizeURL(t *testing.T) {
	got := parseModelIDs(" qwen2.5-0.5b-instruct-q4_k_m, ,qwen2.5-1.5b-instruct-q4_k_m,qwen2.5-0.5b-instruct-q4_k_m ")
	if len(got) != 2 || got[1] != localmodel.ModelQwen15 {
		t.Fatalf("ids = %#v", got)
	}
	if got := normalizeBaseURL("http://127.0.0.1:8421/v1/chat/completions/"); got != "http://127.0.0.1:8421/v1" {
		t.Fatalf("normalized = %q", got)
	}
}
