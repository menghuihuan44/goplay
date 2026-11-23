package localmodel

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCatalogHasThreeTiersWithSplitRoles(t *testing.T) {
	cat := NewCatalog("https://example.test")
	if len(cat.Tiers) != 3 {
		t.Fatalf("tiers = %d, want 3", len(cat.Tiers))
	}
	if got := cat.Endpoint; got != "https://example.test" {
		t.Fatalf("endpoint = %q", got)
	}
	wantRAM := map[Tier]uint64{
		TierLow:    8 * GiB,
		TierMedium: 16 * GiB,
		TierHigh:   32 * GiB,
	}
	for i, tier := range TierOrder() {
		ts, ok := cat.Tier(tier)
		if !ok {
			t.Fatalf("missing tier %q", tier)
		}
		if cat.Tiers[i].ID != tier {
			t.Fatalf("tier order[%d] = %q, want %q", i, cat.Tiers[i].ID, tier)
		}
		if ts.MinRAMBytes != wantRAM[tier] {
			t.Errorf("%s min RAM = %d, want %d", tier, ts.MinRAMBytes, wantRAM[tier])
		}
		if ts.TTFTBudgetMS <= 0 || ts.TPSFloor <= 0 || ts.ContextTokens < 2048 || ts.Threads < 1 {
			t.Errorf("%s has invalid budget/context/threads: %+v", tier, ts)
		}
		if len(ts.Roles) != 3 {
			t.Fatalf("%s roles = %d, want 3", tier, len(ts.Roles))
		}
		for j, role := range Roles() {
			a := ts.Roles[j]
			if a.Role != role {
				t.Errorf("%s role[%d] = %q, want %q", tier, j, a.Role, role)
			}
			if a.Purpose == "" || a.Fallback == "" {
				t.Errorf("%s/%s needs purpose and fallback", tier, role)
			}
			if a.ModelID != "" {
				if _, ok := cat.Model(a.ModelID); !ok {
					t.Errorf("%s/%s references unknown model %q", tier, role, a.ModelID)
				}
			}
		}
		if ts.Roles[2].Mode != ModeRules {
			t.Errorf("%s validator must be deterministic rules, got %q", tier, ts.Roles[2].Mode)
		}
		if ts.Roles[1].Mode != ModeModel || ts.Roles[1].ModelID == "" {
			t.Errorf("%s strategy must use a model", tier)
		}
	}

	if cat.Tiers[0].Roles[0].Mode != ModeRules {
		t.Errorf("low perception must be rules-only")
	}
	if cat.Tiers[1].Roles[0].Mode != ModeHybrid || cat.Tiers[2].Roles[0].Mode != ModeHybrid {
		t.Errorf("medium/high perception should be hybrid rules+small model")
	}
}

func TestCatalogPlanAndRequiredModels(t *testing.T) {
	cat := NewCatalog("")
	for _, tier := range TierOrder() {
		plan, ok := cat.Plan(tier)
		if !ok {
			t.Fatalf("plan %q missing", tier)
		}
		if len(plan.Roles) != 3 || len(plan.Models) == 0 {
			t.Fatalf("plan %q incomplete: roles=%d models=%d", tier, len(plan.Roles), len(plan.Models))
		}
		for _, m := range plan.Models {
			if len(m.SHA256) != 64 {
				t.Errorf("%s SHA256 length = %d", m.ID, len(m.SHA256))
			}
			if _, err := hex.DecodeString(m.SHA256); err != nil {
				t.Errorf("%s SHA256 is not hex: %v", m.ID, err)
			}
			if m.SizeBytes < 100<<20 {
				t.Errorf("%s size %d is implausibly small", m.ID, m.SizeBytes)
			}
			if m.License == "" || m.ContextTokens <= 0 {
				t.Errorf("%s missing license/context: %+v", m.ID, m)
			}
			wantURL := "https://huggingface.co/" + m.Repo + "/resolve/main/" + m.File + "?download=true"
			if got := cat.URL(m); got != wantURL {
				t.Errorf("URL(%s) = %q, want %q", m.ID, got, wantURL)
			}
		}
	}

	low, _ := cat.RequiredModels(TierLow)
	if len(low) != 1 || low[0].ID != ModelQwen05 {
		t.Errorf("low required models = %+v, want just Qwen2.5-0.5B", low)
	}
	medium, _ := cat.RequiredModels(TierMedium)
	if len(medium) != 2 {
		t.Errorf("medium required = %d, want perception+strategy", len(medium))
	}
	high, _ := cat.RequiredModels(TierHigh)
	if len(high) != 2 {
		t.Errorf("high required = %d, want perception+strategy", len(high))
	}
	if _, ok := cat.Plan(Tier("ultra")); ok {
		t.Errorf("unknown tier must not resolve")
	}
}

func TestCatalogMirrorAndLicenses(t *testing.T) {
	t.Setenv("GOPLAY_HF_ENDPOINT", "https://hf-mirror.example/")
	if got := EndpointFromEnv(); got != "https://hf-mirror.example" {
		t.Fatalf("endpoint = %q", got)
	}
	cat := DefaultCatalog()
	m, _ := cat.Model(ModelQwen05)
	if got := cat.URL(m); !strings.HasPrefix(got, "https://hf-mirror.example/") {
		t.Fatalf("mirror URL = %q", got)
	}
	if q, _ := cat.Model(ModelQwen05); q.License != "Apache-2.0" {
		t.Errorf("Qwen license = %q", q.License)
	}
	gemma, _ := cat.Model(ModelGemma)
	if !strings.Contains(gemma.License, "Gemma") {
		t.Errorf("Gemma license = %q, want Gemma Terms of Use", gemma.License)
	}
	if !strings.Contains(strings.ToLower(gemma.Notes), "gated") {
		t.Errorf("Gemma notes should mention the gated original repo")
	}
	if got := ModelURL("", m); !strings.HasPrefix(got, "https://hf-mirror.example/") {
		t.Errorf("empty endpoint should use env mirror, got %q", got)
	}
}

func TestCatalogRouteIsOrderedAndSequential(t *testing.T) {
	cat := NewCatalog("")
	for _, tier := range TierOrder() {
		route, ok := cat.Route(tier)
		if !ok {
			t.Fatalf("route %q missing", tier)
		}
		if len(route) != len(Roles()) {
			t.Fatalf("%s route length = %d, want %d", tier, len(route), len(Roles()))
		}
		for i, want := range Roles() {
			if route[i].Role != want {
				t.Errorf("%s route[%d] = %q, want %q", tier, i, route[i].Role, want)
			}
		}
		// Route must be a copy: callers cannot mutate the catalog's tier spec.
		route[0].Mode = ModeModel
		fresh, _ := cat.Route(tier)
		if fresh[0].Mode == ModeModel {
			t.Errorf("%s route shares mutable backing storage with catalog", tier)
		}
	}
	if _, ok := cat.Route(Tier("ultra")); ok {
		t.Errorf("unknown tier must not resolve")
	}
}

func TestParseTierAndHumanBytes(t *testing.T) {
	for _, in := range []string{"low", " LOW ", "medium", "high"} {
		if _, ok := ParseTier(in); !ok {
			t.Errorf("ParseTier(%q) failed", in)
		}
	}
	for _, in := range []string{"", "auto", "ultra", "lowest"} {
		if _, ok := ParseTier(in); ok {
			t.Errorf("ParseTier(%q) should fail", in)
		}
	}
	cases := []struct {
		n    int64
		want string
	}{
		{-1, "unknown"},
		{0, "0 B"},
		{1023, "1023 B"},
		{1536, "1.5 KiB"},
		{1 << 30, "1.0 GiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestCatalogModelSpecsArePinnedAndSafe(t *testing.T) {
	for _, m := range DefaultCatalog().Models {
		if err := validateModelSpec(m); err != nil {
			t.Errorf("catalog model %s is not downloadable safely: %v", m.ID, err)
		}
	}
}
