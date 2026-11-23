package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// InstanceStatus is the lifecycle state of one game instance.
type InstanceStatus string

const (
	StatusStopped  InstanceStatus = "stopped"
	StatusStarting InstanceStatus = "starting"
	StatusRunning  InstanceStatus = "running"
	StatusPaused   InstanceStatus = "paused"
	StatusHuman    InstanceStatus = "human_control"
	StatusError    InstanceStatus = "error"
)

func (s InstanceStatus) Terminal() bool { return s == StatusStopped || s == StatusError }

// InputSpec describes a low-level input action. It is intentionally
// platform-neutral: Windows uses SendInput, Android uses Accessibility
// gestures. It is never injected into a game process.
type InputSpec struct {
	Type       string  `json:"type"` // click, move, key, text, wait, swipe, tap
	Key        string  `json:"key,omitempty"`
	Text       string  `json:"text,omitempty"`
	X          float64 `json:"x,omitempty"` // 0..1 normalized to capture region
	Y          float64 `json:"y,omitempty"`
	X2         float64 `json:"x2,omitempty"`
	Y2         float64 `json:"y2,omitempty"`
	Button     string  `json:"button,omitempty"`
	DurationMS int     `json:"duration_ms,omitempty"`
	ClickCount int     `json:"click_count,omitempty"`
}

// Action is a semantic action proposed by a policy. Target and Tags are used
// for hard-constraint filtering; Input carries the concrete input mapping.
type Action struct {
	Kind     string         `json:"kind"`
	Target   string         `json:"target,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Expected string         `json:"expected,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Risk     float64        `json:"risk,omitempty"`
	Input    *InputSpec     `json:"input,omitempty"`
}

func (a Action) Key() string { return a.Kind + "|" + a.Target }

func (a Action) HasTag(tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range a.Tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

// State is the perception result for one frame. Values/Text are the only
// fields the decision layer sees; Raw is retained for profiles and debugging.
type State struct {
	Summary    string             `json:"summary"`
	Values     map[string]float64 `json:"values,omitempty"`
	Text       map[string]string  `json:"text,omitempty"`
	Tags       []string           `json:"tags,omitempty"`
	Confidence float64            `json:"confidence"`
	Hash       string             `json:"hash,omitempty"`
	Raw        map[string]any     `json:"raw,omitempty"`
	At         time.Time          `json:"at"`
}

func (s State) Value(k string) float64 {
	if s.Values == nil {
		return 0
	}
	return s.Values[k]
}

func (s State) TextField(k string) string {
	if s.Text == nil {
		return ""
	}
	return s.Text[k]
}

func (s State) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// Frame is a captured RGBA frame. RGBA is top-down, 4 bytes per pixel.
type Frame struct {
	Width  int               `json:"width"`
	Height int               `json:"height"`
	RGBA   []byte            `json:"-"`
	At     time.Time         `json:"at"`
	Source string            `json:"source"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// Observation is the result of executing one action.
type Observation struct {
	State    State     `json:"state"`
	Reward   float64   `json:"reward"`
	Delta    float64   `json:"delta"`
	Changed  bool      `json:"changed"`
	Terminal bool      `json:"terminal"`
	Won      bool      `json:"won"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
}

// Constraint is a hard condition. Conversational constraints are always
// instance-scoped; global preferences are never promoted to constraints.
type Constraint struct {
	ID          string     `json:"id"`
	Text        string     `json:"text"`
	DenyActions []string   `json:"deny_actions,omitempty"`
	DenyTargets []string   `json:"deny_targets,omitempty"`
	DenyTags    []string   `json:"deny_tags,omitempty"`
	Source      string     `json:"source"` // user_text, user_voice, config
	InstanceID  string     `json:"instance_id"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Active      bool       `json:"active"`
}

func (c Constraint) IsActive(now time.Time) bool {
	if !c.Active {
		return false
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return false
	}
	return true
}

// Denies reports whether the action violates this constraint. Targets and
// tags are matched case-insensitively; an empty target in the constraint
// matches every target.
func (c Constraint) Denies(a Action) (bool, string) {
	for _, k := range c.DenyActions {
		if strings.EqualFold(k, a.Kind) {
			if len(c.DenyTargets) == 0 || targetMatches(a.Target, c.DenyTargets) {
				return true, fmt.Sprintf("action %q denied by constraint %q", a.Kind, c.ID)
			}
		}
	}
	for _, t := range c.DenyTags {
		if a.HasTag(t) {
			return true, fmt.Sprintf("tag %q denied by constraint %q", t, c.ID)
		}
	}
	return false, ""
}

func targetMatches(target string, denied []string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, d := range denied {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if d == target || strings.Contains(target, d) || strings.Contains(d, target) {
			return true
		}
	}
	return false
}

// Preference is a cross-game, soft preference. It can never deny an action;
// the instance adapter turns it into weights or drops it on conflict.
type Preference struct {
	ID        string             `json:"id"`
	Text      string             `json:"text"`
	Weights   map[string]float64 `json:"weights,omitempty"`
	AvoidTags []string           `json:"avoid_tags,omitempty"`
	AppliesTo []string           `json:"applies_to,omitempty"` // empty = all games
	Source    string             `json:"source"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// InstanceRules describes the fixed requirements of the current game/scenario.
// A global preference that conflicts with these rules is suppressed by the
// adapter instead of silently overriding the instance.
type InstanceRules struct {
	RequiredTags  []string `json:"required_tags,omitempty"`
	ForbiddenTags []string `json:"forbidden_tags,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type SoftWeights map[string]float64

func (w SoftWeights) Get(k string) float64 {
	if w == nil {
		return 0
	}
	return w[k]
}

func (w SoftWeights) Add(k string, v float64) {
	if w == nil {
		return
	}
	w[k] += v
}

// Adaptation records what the instance adapter did with one global preference.
type Adaptation struct {
	PreferenceID string    `json:"preference_id"`
	Preference   string    `json:"preference"`
	Outcome      string    `json:"outcome"` // applied, suppressed
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
}

// Control is the merged control surface at one decision boundary. Hard comes
// from the instance channel only; Weights/Hints are soft, adapted global
// preferences.
type Control struct {
	Hard        []Constraint `json:"hard"`
	Weights     SoftWeights  `json:"weights"`
	Hints       []string     `json:"hints"`
	Adaptations []Adaptation `json:"adaptations,omitempty"`
}

// Denied returns whether any hard constraint denies the action.
func (c Control) Denied(a Action, now time.Time) (bool, string) {
	for _, k := range c.Hard {
		if !k.IsActive(now) {
			continue
		}
		if denied, reason := k.Denies(a); denied {
			return true, reason
		}
	}
	return false, ""
}

func (c Control) Weight(name string) float64 { return c.Weights.Get(name) }

type InstanceConfig struct {
	ProfileID        string `json:"profile_id"`
	SourceKind       string `json:"source_kind"` // mock, windows, adb, android_http
	Target           string `json:"target,omitempty"`
	TickMS           int    `json:"tick_ms"`
	LLMMode          string `json:"llm_mode,omitempty"` // "", offline, api, local, auto
	MaxActionsPerSec int    `json:"max_actions_per_sec,omitempty"`
	HumanResumeMS    int    `json:"human_resume_ms,omitempty"`
	Seed             int64  `json:"seed,omitempty"`
	ModelTier        string `json:"model_tier,omitempty"`
}

type InstanceStats struct {
	Decisions      int64     `json:"decisions"`
	Actions        int64     `json:"actions"`
	Errors         int64     `json:"errors"`
	Episodes       int64     `json:"episodes"`
	Wins           int64     `json:"wins"`
	Losses         int64     `json:"losses"`
	HumanTakeovers int64     `json:"human_takeovers"`
	Constraints    int64     `json:"constraints"`
	Preferences    int64     `json:"preferences"`
	Reward         float64   `json:"reward"`
	Progress       float64   `json:"progress"`
	BestProgress   float64   `json:"best_progress"`
	LastDecisionAt time.Time `json:"last_decision_at,omitempty"`
	LastAction     string    `json:"last_action,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
}

type GameInstance struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Config    InstanceConfig `json:"config"`
	Status    InstanceStatus `json:"status"`
	Stats     InstanceStats  `json:"stats"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	MemoryDir string         `json:"memory_dir,omitempty"`
}

type DecisionMeta struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model,omitempty"`
	LatencyMS        float64 `json:"latency_ms"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	Fallback         bool    `json:"fallback,omitempty"`
	Reason           string  `json:"reason,omitempty"`
	Boundary         string  `json:"boundary,omitempty"`
	Policy           string  `json:"policy,omitempty"`
}

// GameProfileInfo is static metadata returned to the UI.
type GameProfileInfo struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Games            []string        `json:"games,omitempty"`
	KnownConstraints []InstanceRules `json:"known_constraints,omitempty"`
	Source           string          `json:"source"` // builtin, user
}

// SortedKeys returns deterministic keys for maps used in prompts/tests.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
