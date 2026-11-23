package localmodel

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Tier is a device capability bucket. Tiers are ordered low < medium < high.
type Tier string

const (
	TierLow    Tier = "low"
	TierMedium Tier = "medium"
	TierHigh   Tier = "high"
)

// ParseTier accepts the canonical tier names. "auto" is handled by Recommend.
func ParseTier(s string) (Tier, bool) {
	switch Tier(strings.ToLower(strings.TrimSpace(s))) {
	case TierLow:
		return TierLow, true
	case TierMedium:
		return TierMedium, true
	case TierHigh:
		return TierHigh, true
	default:
		return "", false
	}
}

// TierOrder is the low-to-high order used by the catalog and reports.
func TierOrder() []Tier { return []Tier{TierLow, TierMedium, TierHigh} }

// Role is one job in the local-model pipeline. Roles are intentionally split:
// they are not an ensemble and must not be merged into one "bigger" model.
type Role string

const (
	// RolePerception normalizes OCR/UI text into game state. Rules are the
	// default; a tiny model may disambiguate labels after the rules.
	RolePerception Role = "perception"
	// RoleStrategy chooses the next action from compact state. This is the
	// only role that needs a reasoning-capable model.
	RoleStrategy Role = "strategy"
	// RoleValidator checks schema, whitelist, cooldowns and invariants. It is
	// always deterministic rules and has final authority over the model.
	RoleValidator Role = "validator"
)

// Roles returns the fixed pipeline order: perceive -> reason -> validate.
func Roles() []Role { return []Role{RolePerception, RoleStrategy, RoleValidator} }

// RoleMode says how a role is implemented.
type RoleMode string

const (
	ModeRules  RoleMode = "rules"  // deterministic Go/Lua rules only
	ModeModel  RoleMode = "model"  // one small GGUF served by llama.cpp
	ModeHybrid RoleMode = "hybrid" // rules first, model only for ambiguity
)

// RoleAssignment is the per-tier implementation of one role.
type RoleAssignment struct {
	Role     Role     `json:"role"`
	Mode     RoleMode `json:"mode"`
	ModelID  string   `json:"model_id,omitempty"`
	Purpose  string   `json:"purpose"`
	Fallback string   `json:"fallback,omitempty"`
}

// ModelSpec describes one downloadable GGUF file. SHA256 and SizeBytes are
// pinned to the upstream LFS metadata so downloads are verifiable.
type ModelSpec struct {
	ID            string `json:"id"`
	Repo          string `json:"repo"`
	File          string `json:"file"`
	Quant         string `json:"quant"`
	License       string `json:"license"`
	LicenseURL    string `json:"license_url,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	ContextTokens int    `json:"context_tokens"`
	Notes         string `json:"notes,omitempty"`
}

// SizeGB is the decimal-ish GiB figure shown to users.
func (m ModelSpec) SizeGB() float64 { return float64(m.SizeBytes) / float64(1<<30) }

// TierSpec is a catalog entry. MinRAMBytes is the comfortable minimum; the
// recommendation thresholds include headroom because installed RAM is reported
// lower than the box label (hardware reserved, OS overhead).
type TierSpec struct {
	ID                  Tier             `json:"id"`
	Label               string           `json:"label"`
	Summary             string           `json:"summary"`
	MinRAMBytes         uint64           `json:"min_ram_bytes"`
	RecommendedRAMBytes uint64           `json:"recommended_ram_bytes"`
	TTFTBudgetMS        int              `json:"ttft_budget_ms"`
	TPSFloor            float64          `json:"tps_floor"`
	ContextTokens       int              `json:"context_tokens"`
	Threads             int              `json:"threads"`
	Roles               []RoleAssignment `json:"roles"`
	OptionalModelIDs    []string         `json:"optional_model_ids,omitempty"`
}

// Catalog is the immutable set of supported models and tier plans.
type Catalog struct {
	Endpoint string      `json:"endpoint"`
	Models   []ModelSpec `json:"models"`
	Tiers    []TierSpec  `json:"tiers"`
}

// DefaultEndpoint is the canonical Hugging Face origin.
const DefaultEndpoint = "https://huggingface.co"

// EndpointFromEnv returns GOPLAY_HF_ENDPOINT when set (for example a mirror
// such as https://hf-mirror.com), otherwise DefaultEndpoint.
func EndpointFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("GOPLAY_HF_ENDPOINT")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultEndpoint
}

// ModelURL builds the direct GGUF URL for a model, honoring a mirror endpoint.
// The ?download=true suffix is harmless for direct HTTP clients and matches the
// links shown by the Hugging Face UI.
func ModelURL(endpoint string, m ModelSpec) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		e = EndpointFromEnv()
	}
	return e + "/" + m.Repo + "/resolve/main/" + url.PathEscape(m.File) + "?download=true"
}

// URL is ModelURL for the catalog's endpoint.
func (c Catalog) URL(m ModelSpec) string { return ModelURL(c.Endpoint, m) }

// Model finds a model by ID.
func (c Catalog) Model(id string) (ModelSpec, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelSpec{}, false
}

// Tier finds a tier by ID.
func (c Catalog) Tier(t Tier) (TierSpec, bool) {
	for _, ts := range c.Tiers {
		if ts.ID == t {
			return ts, true
		}
	}
	return TierSpec{}, false
}

// TierPlan is a fully resolved tier: role assignments plus model metadata and
// direct URLs. It is the shape printed by "goplay-model recommend".
type TierPlan struct {
	Tier                Tier             `json:"tier"`
	Label               string           `json:"label"`
	Summary             string           `json:"summary"`
	MinRAMBytes         uint64           `json:"min_ram_bytes"`
	MinRAMGB            float64          `json:"min_ram_gb"`
	RecommendedRAMBytes uint64           `json:"recommended_ram_bytes"`
	RecommendedRAMGB    float64          `json:"recommended_ram_gb"`
	TTFTBudgetMS        int              `json:"ttft_budget_ms"`
	TPSFloor            float64          `json:"tps_floor"`
	ContextTokens       int              `json:"context_tokens"`
	Threads             int              `json:"threads"`
	Roles               []RoleAssignment `json:"roles"`
	Models              []ModelSpec      `json:"models"`
	OptionalModels      []ModelSpec      `json:"optional_models,omitempty"`
}

// Role returns the assignment for a role.
func (p TierPlan) Role(role Role) (RoleAssignment, bool) {
	for _, a := range p.Roles {
		if a.Role == role {
			return a, true
		}
	}
	return RoleAssignment{}, false
}

// Plan resolves a tier into models and role assignments.
func (c Catalog) Plan(t Tier) (TierPlan, bool) {
	ts, ok := c.Tier(t)
	if !ok {
		return TierPlan{}, false
	}
	seen := map[string]bool{}
	models := make([]ModelSpec, 0, len(ts.Roles))
	for _, a := range ts.Roles {
		if a.ModelID == "" || seen[a.ModelID] {
			continue
		}
		m, ok := c.Model(a.ModelID)
		if !ok {
			continue
		}
		seen[a.ModelID] = true
		models = append(models, m)
	}
	optional := make([]ModelSpec, 0, len(ts.OptionalModelIDs))
	for _, id := range ts.OptionalModelIDs {
		if m, ok := c.Model(id); ok {
			optional = append(optional, m)
		}
	}
	return TierPlan{
		Tier:                ts.ID,
		Label:               ts.Label,
		Summary:             ts.Summary,
		MinRAMBytes:         ts.MinRAMBytes,
		MinRAMGB:            float64(ts.MinRAMBytes) / float64(1<<30),
		RecommendedRAMBytes: ts.RecommendedRAMBytes,
		RecommendedRAMGB:    float64(ts.RecommendedRAMBytes) / float64(1<<30),
		TTFTBudgetMS:        ts.TTFTBudgetMS,
		TPSFloor:            ts.TPSFloor,
		ContextTokens:       ts.ContextTokens,
		Threads:             ts.Threads,
		Roles:               append([]RoleAssignment(nil), ts.Roles...),
		Models:              models,
		OptionalModels:      optional,
	}, true
}

// Route returns the ordered role assignments for a tier. This is the router
// decision consumed by the engine: perception, then strategy, then validator.
// The roles are sequential stages, not an ensemble and not additive capability.
func (c Catalog) Route(t Tier) ([]RoleAssignment, bool) {
	p, ok := c.Plan(t)
	if !ok {
		return nil, false
	}
	return append([]RoleAssignment(nil), p.Roles...), true
}

// RequiredModels returns the models that must be downloaded for a tier. Only
// role implementations using ModeModel are included; optional alternatives are
// never downloaded automatically.
func (c Catalog) RequiredModels(t Tier) ([]ModelSpec, bool) {
	p, ok := c.Plan(t)
	if !ok {
		return nil, false
	}
	return p.Models, true
}

// Model IDs pinned in this catalog. They are also the on-disk file stems.
const (
	ModelQwen05 = "qwen2.5-0.5b-instruct-q4_k_m"
	ModelQwen15 = "qwen2.5-1.5b-instruct-q4_k_m"
	ModelQwen3  = "qwen2.5-3b-instruct-q4_k_m"
	ModelSmol   = "smollm2-360m-instruct-q8_0"
	ModelGemma  = "gemma-3-270m-it-q4_0"
)

// NewCatalog returns the built-in catalog. Sizes and SHA256 values come from the
// Hugging Face LFS metadata (queried 2026-11-23); mirrors must serve identical
// bytes or checksum verification will fail, which is intentional.
func NewCatalog(endpoint string) Catalog {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = EndpointFromEnv()
	}
	models := []ModelSpec{
		{
			ID:            ModelQwen05,
			Repo:          "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
			File:          "qwen2.5-0.5b-instruct-q4_k_m.gguf",
			Quant:         "Q4_K_M",
			License:       "Apache-2.0",
			LicenseURL:    "https://www.apache.org/licenses/LICENSE-2.0",
			SizeBytes:     491400032,
			SHA256:        "74a4da8c9fdbcd15bd1f6d01d621410d31c6fc00986f5eb687824e7b93d7a9db",
			ContextTokens: 32768,
			Notes:         "Instruction-tuned Qwen2.5; good short-horizon JSON action choices.",
		},
		{
			ID:            ModelQwen15,
			Repo:          "Qwen/Qwen2.5-1.5B-Instruct-GGUF",
			File:          "qwen2.5-1.5b-instruct-q4_k_m.gguf",
			Quant:         "Q4_K_M",
			License:       "Apache-2.0",
			LicenseURL:    "https://www.apache.org/licenses/LICENSE-2.0",
			SizeBytes:     1117320736,
			SHA256:        "6a1a2eb6d15622bf3c96857206351ba97e1af16c30d7a74ee38970e434e9407e",
			ContextTokens: 32768,
			Notes:         "Balanced reasoning tier for mid-range CPUs.",
		},
		{
			ID:            ModelQwen3,
			Repo:          "Qwen/Qwen2.5-3B-Instruct-GGUF",
			File:          "qwen2.5-3b-instruct-q4_k_m.gguf",
			Quant:         "Q4_K_M",
			License:       "Apache-2.0",
			LicenseURL:    "https://www.apache.org/licenses/LICENSE-2.0",
			SizeBytes:     2104932768,
			SHA256:        "626b4a6678b86442240e33df819e00132d3ba7dddfe1cdc4fbb18e0a9615c62d",
			ContextTokens: 32768,
			Notes:         "Strongest catalog model; still small enough for CPU-only high tier.",
		},
		{
			ID:            ModelSmol,
			Repo:          "HuggingFaceTB/SmolLM2-360M-Instruct-GGUF",
			File:          "smollm2-360m-instruct-q8_0.gguf",
			Quant:         "Q8_0",
			License:       "Apache-2.0",
			LicenseURL:    "https://www.apache.org/licenses/LICENSE-2.0",
			SizeBytes:     386404992,
			SHA256:        "48ab3034d0dd401fbc721eb1df3217902fee7dab9078992d66431f09b7750201",
			ContextTokens: 8192,
			Notes:         "Tiny text filter used after deterministic perception rules.",
		},
		{
			ID:            ModelGemma,
			Repo:          "unsloth/gemma-3-270m-it-GGUF",
			File:          "gemma-3-270m-it-Q4_0.gguf",
			Quant:         "Q4_0",
			License:       "Gemma Terms of Use",
			LicenseURL:    "https://ai.google.dev/gemma/terms",
			SizeBytes:     241574944,
			SHA256:        "e479ea2962bdcdc7e6321b91148b9ac2f516f649e0921412561d4936aadef158",
			ContextTokens: 32768,
			Notes:         "Optional perception alternative; derivative of google/gemma-3-270m-it. Gemma Terms are not OSI-approved and the original repo is gated.",
		},
	}

	tiers := []TierSpec{
		{
			ID:                  TierLow,
			Label:               "Low - rule-first",
			Summary:             "No perception LLM: deterministic HUD/OCR rules, a 0.5B strategy model, and a deterministic validator. This is the smallest first-run download and is not an ensemble.",
			MinRAMBytes:         8 << 30,
			RecommendedRAMBytes: 8 << 30,
			TTFTBudgetMS:        400,
			TPSFloor:            25,
			ContextTokens:       2048,
			Threads:             4,
			Roles: []RoleAssignment{
				{
					Role:     RolePerception,
					Mode:     ModeRules,
					Purpose:  "Parse/normalize OCR and HUD text with Go/Lua rules; no model call on the hot path.",
					Fallback: "Same rules; mark unparsed fields as unknown.",
				},
				{
					Role:     RoleStrategy,
					Mode:     ModeModel,
					ModelID:  ModelQwen05,
					Purpose:  "Pick one short-horizon action from a compact JSON state.",
					Fallback: "Heuristic policy and Lua rules.",
				},
				{
					Role:     RoleValidator,
					Mode:     ModeRules,
					Purpose:  "Schema, action whitelist, cooldown and invariant checks. Authoritative over the model.",
					Fallback: "Reject the action.",
				},
			},
			OptionalModelIDs: []string{ModelSmol, ModelGemma},
		},
		{
			ID:                  TierMedium,
			Label:               "Medium - tiny filter + 1.5B",
			Summary:             "Rules prefilter perception, a 360M model resolves ambiguous labels, a 1.5B model plans, and rules validate. Roles run in sequence; the models are not voted together.",
			MinRAMBytes:         16 << 30,
			RecommendedRAMBytes: 16 << 30,
			TTFTBudgetMS:        600,
			TPSFloor:            15,
			ContextTokens:       4096,
			Threads:             4,
			Roles: []RoleAssignment{
				{
					Role:     RolePerception,
					Mode:     ModeHybrid,
					ModelID:  ModelSmol,
					Purpose:  "Rules handle known text; SmolLM2-360M only disambiguates OCR typos or unknown labels.",
					Fallback: "Rules only.",
				},
				{
					Role:     RoleStrategy,
					Mode:     ModeModel,
					ModelID:  ModelQwen15,
					Purpose:  "Better multi-step planning while staying responsive on 4 CPU threads.",
					Fallback: "Low-tier 0.5B model or heuristic policy.",
				},
				{
					Role:     RoleValidator,
					Mode:     ModeRules,
					Purpose:  "Deterministic safety and legality checks; never delegated to a model.",
					Fallback: "Reject the action.",
				},
			},
			OptionalModelIDs: []string{ModelGemma},
		},
		{
			ID:                  TierHigh,
			Label:               "High - filter + 3B",
			Summary:             "A 0.5B perception filter and a 3B strategy model, with rules as validator. Run roles sequentially (or on separate loopback ports); do not sum model votes.",
			MinRAMBytes:         32 << 30,
			RecommendedRAMBytes: 32 << 30,
			TTFTBudgetMS:        900,
			TPSFloor:            8,
			ContextTokens:       8192,
			Threads:             6,
			Roles: []RoleAssignment{
				{
					Role:     RolePerception,
					Mode:     ModeHybrid,
					ModelID:  ModelQwen05,
					Purpose:  "Resolve harder OCR/UI ambiguity after rules; output is compact structured state.",
					Fallback: "SmolLM2-360M or rules only.",
				},
				{
					Role:     RoleStrategy,
					Mode:     ModeModel,
					ModelID:  ModelQwen3,
					Purpose:  "Longer-horizon planning and constraint-aware action selection.",
					Fallback: "Medium-tier 1.5B model.",
				},
				{
					Role:     RoleValidator,
					Mode:     ModeRules,
					Purpose:  "Deterministic schema/whitelist/cooldown/invariant gate; final authority.",
					Fallback: "Reject the action.",
				},
			},
			OptionalModelIDs: []string{ModelSmol},
		},
	}
	return Catalog{Endpoint: strings.TrimRight(endpoint, "/"), Models: models, Tiers: tiers}
}

// DefaultCatalog returns the built-in catalog using GOPLAY_HF_ENDPOINT if set.
func DefaultCatalog() Catalog { return NewCatalog(EndpointFromEnv()) }

// HumanBytes renders a byte count with binary units.
func HumanBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
