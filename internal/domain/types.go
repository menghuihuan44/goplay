package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	Decisions        int64     `json:"decisions"`
	Actions          int64     `json:"actions"`
	EffectiveActions int64     `json:"effective_actions"`
	Errors           int64     `json:"errors"`
	Episodes         int64     `json:"episodes"`
	Wins             int64     `json:"wins"`
	Losses           int64     `json:"losses"`
	HumanTakeovers   int64     `json:"human_takeovers"`
	Constraints      int64     `json:"constraints"`
	Preferences      int64     `json:"preferences"`
	Reward           float64   `json:"reward"`
	Progress         float64   `json:"progress"`
	BestProgress     float64   `json:"best_progress"`
	LastDecisionAt   time.Time `json:"last_decision_at,omitempty"`
	LastAction       string    `json:"last_action,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
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

// DisplayField is one human-readable row in the generic instance HUD. Kind is
// one of "number", "text", "percent" or "status"; the UI may use it for styling
// but must not assume a fixed game vocabulary.
type DisplayField struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	Kind      string `json:"kind"`
	Highlight bool   `json:"highlight,omitempty"`
}

// DisplayView is a game-agnostic projection of the latest perceived state. It
// is rebuilt from State.Values/Text on every snapshot: games without health,
// score, rooms or any other familiar concept simply produce the fields they do
// have, and never a fabricated health bar or outcome.
type DisplayView struct {
	Summary       string         `json:"summary"`
	Fields        []DisplayField `json:"fields"`
	Tags          []string       `json:"tags,omitempty"`
	Outcome       *string        `json:"outcome"`
	Progress      *float64       `json:"progress"`
	ProgressLabel string         `json:"progress_label,omitempty"`
	LastAction    string         `json:"last_action,omitempty"`
	LastReason    string         `json:"last_reason,omitempty"`
	Notes         []string       `json:"notes,omitempty"`
}

const maxDisplayFields = 24

var displayLabels = map[string]string{
	"hp": "生命", "max_hp": "最大生命", "health": "生命",
	"score": "分数", "level": "等级", "timer": "时间", "gold": "金币",
	"xp": "经验", "ammo": "弹药", "room": "房间", "rooms": "房间数",
	"wave": "波次", "mana": "法力",
}

// BuildDisplay projects one perception state into UI fields. Fields are derived
// dynamically from every Values/Text entry, sorted by key and capped at 24.
// Progress is only reported for explicit progress-like keys; Outcome is only
// reported when episode stats or tags prove one, so unknown games stay honest.
func BuildDisplay(st State, stats InstanceStats, lastAction, lastReason string) DisplayView {
	dv := DisplayView{
		Summary: st.Summary, LastAction: lastAction, LastReason: lastReason,
		Tags: append([]string(nil), st.Tags...),
	}
	keys := make(map[string]struct{}, len(st.Values)+len(st.Text))
	for k := range st.Values {
		keys[k] = struct{}{}
	}
	for k := range st.Text {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		if len(dv.Fields) >= maxDisplayFields {
			break
		}
		if field, ok := displayField(st, k); ok {
			dv.Fields = append(dv.Fields, field)
		}
	}
	dv.Outcome = displayOutcome(st, stats)
	dv.Progress, dv.ProgressLabel = displayProgress(st)
	return dv
}

func displayField(st State, key string) (DisplayField, bool) {
	label := displayLabel(key)
	if v, ok := st.Text[key]; ok && strings.TrimSpace(v) != "" {
		kind := "text"
		if isStatusKey(key) {
			kind = "status"
		}
		return DisplayField{Key: key, Label: label, Value: v, Kind: kind, Highlight: highlightKey(key)}, true
	}
	v, ok := st.Values[key]
	if !ok {
		return DisplayField{}, false
	}
	kind := "number"
	value := formatDisplayNumber(v)
	if isPercentKey(key) {
		kind = "percent"
		value = formatDisplayPercent(v)
	}
	return DisplayField{Key: key, Label: label, Value: value, Kind: kind, Highlight: highlightKey(key)}, true
}

func displayLabel(key string) string {
	lower := strings.ToLower(strings.TrimSpace(key))
	if label, ok := displayLabels[lower]; ok {
		return label
	}
	var b strings.Builder
	prevLower := false
	for _, r := range key {
		if r == '_' || r == '-' || r == ' ' || r == '.' {
			b.WriteByte(' ')
			prevLower = false
			continue
		}
		if unicode.IsUpper(r) && prevLower {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
	}
	parts := strings.Fields(b.String())
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "hp":
			parts[i] = "HP"
		case "xp":
			parts[i] = "XP"
		case "id":
			parts[i] = "ID"
		default:
			runes := []rune(p)
			if len(runes) > 0 {
				runes[0] = unicode.ToUpper(runes[0])
				parts[i] = string(runes)
			}
		}
	}
	if len(parts) == 0 {
		return key
	}
	return strings.Join(parts, " ")
}

func formatDisplayNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func formatDisplayPercent(v float64) string {
	p := v
	if p >= 0 && p <= 1 {
		p *= 100
	}
	return formatDisplayNumber(p) + "%"
}

func isPercentKey(key string) bool {
	switch strings.ToLower(key) {
	case "progress", "pct", "percent":
		return true
	}
	return false
}

func isStatusKey(key string) bool {
	switch strings.ToLower(key) {
	case "phase", "status", "state", "result", "outcome":
		return true
	}
	return false
}

func highlightKey(key string) bool {
	switch strings.ToLower(key) {
	case "hp", "health", "score", "progress", "pct", "percent",
		"room", "level", "wave", "stage", "timer":
		return true
	}
	return false
}

func displayOutcome(st State, stats InstanceStats) *string {
	outcome := ""
	switch {
	case stats.Wins > 0:
		outcome = "胜"
	case stats.Losses > 0:
		outcome = "负"
	case st.HasTag("won") || st.HasTag("win") || st.HasTag("victory"):
		outcome = "胜"
	case st.HasTag("lost") || st.HasTag("lose") || st.HasTag("dead") || st.HasTag("game_over") || st.HasTag("gameover"):
		outcome = "负"
	case st.HasTag("terminal"):
		outcome = "进行中"
	}
	if outcome == "" {
		return nil
	}
	return &outcome
}

func displayProgress(st State) (*float64, string) {
	for _, key := range []string{"progress", "pct", "percent"} {
		v, ok := st.Values[key]
		if !ok {
			continue
		}
		if p, ok := normalizedProgress(v); ok {
			return &p, "进度 " + formatDisplayPercent(v)
		}
	}
	for _, candidate := range []struct {
		key     string
		maxKeys []string
	}{
		{"room", []string{"rooms", "max_rooms", "total_rooms"}},
		{"level", []string{"max_level", "levels", "total_levels"}},
		{"wave", []string{"max_wave", "waves", "total_waves"}},
		{"stage", []string{"max_stage", "stages", "total_stages"}},
	} {
		cur, ok := st.Values[candidate.key]
		if !ok {
			continue
		}
		max, ok := firstPositiveValue(st, candidate.maxKeys)
		if !ok {
			continue
		}
		if p, ok := ratioProgress(cur, max); ok {
			label := fmt.Sprintf("%s %s/%s", displayLabel(candidate.key), formatDisplayNumber(cur), formatDisplayNumber(max))
			return &p, label
		}
	}
	return nil, ""
}

func normalizedProgress(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	switch {
	case v >= 0 && v <= 1:
		return v, true
	case v > 1 && v <= 100:
		return v / 100, true
	}
	return 0, false
}

func ratioProgress(cur, max float64) (float64, bool) {
	if math.IsNaN(cur) || math.IsInf(cur, 0) || math.IsNaN(max) || math.IsInf(max, 0) {
		return 0, false
	}
	if cur < 0 || max <= 0 || cur > max {
		return 0, false
	}
	return cur / max, true
}

func firstPositiveValue(st State, keys []string) (float64, bool) {
	for _, key := range keys {
		if v, ok := st.Values[key]; ok && v > 0 {
			return v, true
		}
	}
	return 0, false
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
