// Package policy turns a state plus the game's legal actions into one action.
// It combines a contextual UCB bandit (cheap, always available) with an
// optional LLM policy (for unfamiliar situations), then runs every choice
// through a validator. Constraints and preferences arrive pre-merged in
// domain.Control: hard constraints only deny; preferences only shift scores.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/llm"
	"goplay/internal/metrics"
)

type Config struct {
	UCB      float64
	Prior    float64
	LLMEvery int
	MaxRisk  float64
	Seed     int64
	MaxCands int
}

func DefaultConfig() Config {
	return Config{UCB: 0.9, Prior: 0.4, LLMEvery: 12, MaxRisk: 0.9, Seed: 1, MaxCands: 24}
}

type Candidate struct {
	Action domain.Action
	Score  float64
	Mean   float64
	Visits int
	Note   string
}

type Decision struct {
	Action     domain.Action
	Score      float64
	Source     string // heuristic, llm, fallback
	StateKey   string
	ActionKey  string
	Candidates []Candidate
	Reason     string
	Provider   string
	Model      string
	Fallback   bool
	LatencyMS  float64
	LLMUsed    bool
}

type Input struct {
	State    domain.State
	Actions  []domain.Action
	Control  domain.Control
	Rules    domain.InstanceRules
	Hints    []string
	ForceLLM bool
}

type arm struct {
	visits int
	mean   float64
}

type Validator struct {
	AllowedInput map[string]bool
	MaxRisk      float64
}

func NewValidator(maxRisk float64) Validator {
	return Validator{
		AllowedInput: map[string]bool{
			"key": true, "click": true, "tap": true, "swipe": true,
			"text": true, "wait": true, "gamepad": true, "touch": true,
		},
		MaxRisk: maxRisk,
	}
}

func (v Validator) Validate(a domain.Action, legal []domain.Action) error {
	if a.Kind == "" {
		return fmt.Errorf("empty action kind")
	}
	found := false
	for _, l := range legal {
		if l.Key() == a.Key() {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("action %q is not in the current legal set", a.Key())
	}
	if a.Input != nil && a.Input.Type != "" && !v.AllowedInput[a.Input.Type] {
		return fmt.Errorf("input type %q is not allowlisted", a.Input.Type)
	}
	if v.MaxRisk > 0 && a.Risk > v.MaxRisk {
		return fmt.Errorf("risk %.2f exceeds cap %.2f", a.Risk, v.MaxRisk)
	}
	if a.Input != nil && (a.Input.Type == "click" || a.Input.Type == "tap") {
		if a.Input.X < 0 || a.Input.X > 1 || a.Input.Y < 0 || a.Input.Y > 1 {
			return fmt.Errorf("normalized click coordinates out of range: %.3f,%.3f", a.Input.X, a.Input.Y)
		}
	}
	return nil
}

type Policy struct {
	cfg   Config
	llm   llm.Provider
	reg   *metrics.Registry
	val   Validator
	mu    sync.Mutex
	q     map[string]map[string]*arm
	total int
	rng   *rand.Rand
	stats PolicyStats

	// idleStreak counts consecutive wait/back decisions. A screen-reading agent
	// that keeps choosing "wait" looks alive but never touches the game, so after
	// a couple of passive choices the next decision must try an active action.
	idleStreak int
}

type PolicyStats struct {
	Decisions    int64  `json:"decisions"`
	LLMCalls     int64  `json:"llm_calls"`
	LLMFails     int64  `json:"llm_fails"`
	Fallbacks    int64  `json:"fallbacks"`
	LastLLMError string `json:"last_llm_error,omitempty"`
}

func New(provider llm.Provider, reg *metrics.Registry, cfg Config) *Policy {
	if cfg.UCB <= 0 {
		cfg.UCB = 0.9
	}
	if cfg.Prior == 0 {
		cfg.Prior = 0.4
	}
	if cfg.LLMEvery <= 0 {
		cfg.LLMEvery = 12
	}
	if cfg.MaxRisk <= 0 {
		cfg.MaxRisk = 0.9
	}
	if cfg.MaxCands <= 0 {
		cfg.MaxCands = 24
	}
	if cfg.Seed == 0 {
		cfg.Seed = 1
	}
	return &Policy{
		cfg: cfg, llm: provider, reg: reg, val: NewValidator(cfg.MaxRisk),
		q: map[string]map[string]*arm{}, rng: rand.New(rand.NewSource(cfg.Seed)),
	}
}

// StateKey is coarse on purpose: a bandit needs repeated visits to learn, and
// pixel-perfect states would never repeat.
func StateKey(s domain.State) string {
	phase := s.Text["phase"]
	if phase == "" {
		phase = "unknown"
	}
	if len(s.Tags) > 0 && phase == "unknown" {
		phase = s.Tags[0]
	}
	hpBucket := 0
	if max := s.Values["max_hp"]; max > 0 {
		frac := s.Values["hp"] / max
		switch {
		case frac < 0.25:
			hpBucket = 1
		case frac < 0.5:
			hpBucket = 2
		case frac < 0.75:
			hpBucket = 3
		default:
			hpBucket = 4
		}
	}
	rooms := 0
	switch r := s.Values["room"]; {
	case r >= 9:
		rooms = 3
	case r >= 5:
		rooms = 2
	case r >= 3:
		rooms = 1
	}
	return fmt.Sprintf("%s|hp%d|room%d", phase, hpBucket, rooms)
}

func (p *Policy) Decide(ctx context.Context, in Input) Decision {
	start := time.Now()
	key := StateKey(in.State)
	legal := p.filter(in)
	cands := p.score(key, in.State, legal, in.Control)
	d := Decision{StateKey: key, Candidates: cands}

	useLLM := p.llm != nil && (in.ForceLLM || p.shouldLLM() || len(cands) == 0)
	if useLLM {
		if a, resp, err := p.askLLM(ctx, in, legal); err == nil {
			d.Action = a
			d.Source = "llm"
			d.Provider = resp.Provider
			d.Model = resp.Model
			d.LLMUsed = true
			d.Score = 1
			d.Reason = "LLM policy choice"
			p.stats.LLMCalls++
		} else {
			p.stats.LLMFails++
			p.stats.LastLLMError = short(err.Error(), 240)
			d.Fallback = true
			d.Reason = "LLM unavailable: " + err.Error()
			if p.reg != nil {
				p.reg.Inc(metrics.PolicyFallbackTotal, 1)
			}
		}
	}

	if d.Action.Kind == "" {
		if len(cands) > 0 {
			best := cands[0]
			for _, c := range cands[1:] {
				if c.Score > best.Score {
					best = c
				}
			}
			d.Action = best.Action
			d.Score = best.Score
			d.Source = "heuristic"
			if d.Fallback {
				d.Reason += "; heuristic fallback"
			} else {
				d.Reason = "UCB + soft preference weights"
			}
		} else {
			d.Action = fallbackWait()
			d.Source = "fallback"
			d.Fallback = true
			d.Reason = "no legal action after constraints"
			p.stats.Fallbacks++
		}
	}

	p.mu.Lock()
	idle := p.idleStreak
	p.mu.Unlock()
	if idle >= 2 && isPassive(d.Action) {
		if alt, ok := bestActive(cands); ok {
			d.Action = alt.Action
			d.Score = alt.Score
			d.Source = "explore"
			d.Reason = fmt.Sprintf("连续 %d 次等待/返回后强制探索：%s", idle, alt.Action.Key())
		}
	}

	if err := p.val.Validate(d.Action, legal); err != nil && d.Action.Kind != "wait" {
		d.Fallback = true
		d.Source = "fallback"
		d.Reason = "validator rejected: " + err.Error()
		d.Action = bestLegalOrWait(legal)
	}
	d.ActionKey = d.Action.Key()
	d.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
	p.mu.Lock()
	if isPassive(d.Action) {
		p.idleStreak++
	} else {
		p.idleStreak = 0
	}
	p.stats.Decisions++
	p.mu.Unlock()
	if p.reg != nil {
		p.reg.Observe(metrics.DecisionLatencyMS, d.LatencyMS)
		if d.Fallback {
			p.reg.Inc(metrics.PolicyFallbackTotal, 1)
		}
	}
	return d
}

// isPassive marks actions that do not interact with the game. "back" is treated
// as passive too: in an unfamiliar game it can leave the match instead of
// advancing it, so forced exploration prefers taps/swipes.
func isPassive(a domain.Action) bool {
	return a.Kind == "wait" || a.Kind == "back" || a.HasTag("idle")
}

// bestActive returns the highest-scored non-passive candidate from the already
// sorted list. It is only a nudge: if the profile offers no active action the
// decision stays unchanged.
func bestActive(cands []Candidate) (Candidate, bool) {
	for _, c := range cands {
		if !isPassive(c.Action) {
			return c, true
		}
	}
	return Candidate{}, false
}

// filter applies only hard vetoes: instance constraints, forbidden tags, and
// malformed actions. Soft preferences never remove a candidate.
func (p *Policy) filter(in Input) []domain.Action {
	now := time.Now()
	forbidden := map[string]bool{}
	for _, t := range in.Rules.ForbiddenTags {
		forbidden[strings.ToLower(t)] = true
	}
	var out []domain.Action
	for _, a := range in.Actions {
		if a.Kind == "" {
			continue
		}
		if denied, _ := in.Control.Denied(a, now); denied {
			continue
		}
		bad := false
		for _, t := range a.Tags {
			if forbidden[strings.ToLower(t)] {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		out = append(out, a)
		if len(out) >= p.cfg.MaxCands {
			break
		}
	}
	return out
}

func (p *Policy) score(key string, st domain.State, legal []domain.Action, ctl domain.Control) []Candidate {
	c := p.cfg.UCB * (1 + 0.5*ctl.Weight("exploration"))
	if c <= 0 {
		c = p.cfg.UCB
	}
	p.mu.Lock()
	arms := p.q[key]
	total := p.total
	out := make([]Candidate, 0, len(legal))
	for _, a := range legal {
		ak := a.Key()
		var ar *arm
		if arms != nil {
			ar = arms[ak]
		}
		mean, visits := p.cfg.Prior, 0
		if ar != nil {
			mean, visits = ar.mean, ar.visits
		}
		bonus := c * math.Sqrt(math.Log(float64(total)+1)/float64(visits+1))
		adj := softAdjust(a, ctl) + survivalAdjust(a, st)
		out = append(out, Candidate{
			Action: a,
			Score:  mean + bonus + adj,
			Mean:   mean,
			Visits: visits,
		})
	}
	p.mu.Unlock()
	// Stable sort by score, then action key, so the reported order is
	// reproducible when scores tie.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[i].Score || (out[j].Score == out[i].Score && out[j].Action.Key() < out[i].Action.Key()) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// survivalAdjust is a small, game-agnostic safety prior: when perceived HP is
// low, healing outranks damage. It is a baseline, not a replacement for
// learned values, and it never removes a legal action.
func survivalAdjust(a domain.Action, st domain.State) float64 {
	maxHP := st.Value("max_hp")
	if maxHP <= 0 {
		return 0
	}
	frac := st.Value("hp") / maxHP
	if frac >= 0.4 {
		return 0
	}
	if a.Kind == "heal" || a.HasTag("heal") {
		return 4.0
	}
	if a.HasTag("guard") && frac < 0.2 {
		return 1.0
	}
	return 0
}

func softAdjust(a domain.Action, ctl domain.Control) float64 {
	adj := 0.0
	aggr := ctl.Weight("aggression")
	if a.HasTag("combat") || a.HasTag("damage") {
		adj += 0.8 * aggr
	}
	if a.HasTag("guard") {
		adj -= 0.4 * aggr
	}
	adj -= 2.0 * ctl.Weight("risk_aversion") * a.Risk
	if a.HasTag("advance") || a.HasTag("progress") {
		adj += 0.6 * ctl.Weight("speed")
	}
	if a.HasTag("heal") || a.Kind == "heal" {
		adj -= 0.5 * ctl.Weight("resource_aversion")
	}
	adj += ctl.Weight(a.Kind)
	if adj > 1.5 {
		adj = 1.5
	}
	if adj < -1.5 {
		adj = -1.5
	}
	return adj
}

func (p *Policy) Update(d Decision, reward float64) {
	p.UpdateKey(d.StateKey, d.ActionKey, reward)
}

func (p *Policy) UpdateKey(stateKey, actionKey string, reward float64) {
	if stateKey == "" || actionKey == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	arms := p.q[stateKey]
	if arms == nil {
		arms = map[string]*arm{}
		p.q[stateKey] = arms
	}
	ar := arms[actionKey]
	if ar == nil {
		ar = &arm{}
		arms[actionKey] = ar
	}
	ar.visits++
	ar.mean += (reward - ar.mean) / float64(ar.visits)
	p.total++
}

func (p *Policy) shouldLLM() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total%p.cfg.LLMEvery == 0
}

func (p *Policy) Stats() PolicyStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Policy) QSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, arms := range p.q {
		n += len(arms)
	}
	return n
}

func (p *Policy) askLLM(ctx context.Context, in Input, legal []domain.Action) (domain.Action, llm.Response, error) {
	prompt := buildPrompt(in, legal)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := p.llm.Complete(ctx, llm.Request{
		System:      "You are the policy head of an external game-playing agent. You see only a game state summary. Choose exactly one of the listed legal actions. Never invent actions. Respect hard constraints absolutely. Preferences are soft hints. Reply with JSON only.",
		Prompt:      prompt,
		Temperature: 0.2,
		MaxTokens:   160,
		JSON:        true,
	})
	if err != nil {
		return domain.Action{}, resp, err
	}
	js, ok := llm.ExtractJSON(resp.Text)
	if !ok {
		return domain.Action{}, resp, fmt.Errorf("no JSON in response: %q", short(resp.Text, 120))
	}
	var pick struct {
		Kind       string  `json:"kind"`
		Action     string  `json:"action"`
		Target     string  `json:"target"`
		Reason     string  `json:"reason"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(js), &pick); err != nil {
		return domain.Action{}, resp, fmt.Errorf("bad JSON: %w", err)
	}
	kind := pick.Kind
	if kind == "" {
		kind = pick.Action
	}
	if a, ok := groundLLMAction(kind, pick.Target, legal); ok {
		return a, resp, nil
	}
	return domain.Action{}, resp, fmt.Errorf("LLM picked illegal action kind=%q target=%q", kind, pick.Target)
}

// groundLLMAction maps a small model's choice onto the exact legal action set.
// Small models often echo an annotated target ("enemy 10/28") or omit the
// target. The rule layer owns legality, so we accept a kind+target match and
// fall back to the unique legal action of that kind instead of rejecting a
// usable choice. This never invents an action: the result always comes from
// legal.
func groundLLMAction(kind, target string, legal []domain.Action) (domain.Action, bool) {
	kind = strings.TrimSpace(kind)
	target = strings.TrimSpace(target)
	if kind == "" {
		return domain.Action{}, false
	}
	var sameKind []domain.Action
	for _, a := range legal {
		if !strings.EqualFold(a.Kind, kind) {
			continue
		}
		if target != "" && strings.EqualFold(a.Target, target) {
			return a, true
		}
		sameKind = append(sameKind, a)
	}
	if len(sameKind) == 1 {
		return sameKind[0], true
	}
	if target != "" {
		lt := strings.ToLower(target)
		for _, a := range sameKind {
			at := strings.ToLower(a.Target)
			if at != "" && (strings.Contains(lt, at) || strings.Contains(at, lt)) {
				return a, true
			}
		}
	}
	return domain.Action{}, false
}

func buildPrompt(in Input, legal []domain.Action) string {
	var b strings.Builder
	b.WriteString("STATE: ")
	b.WriteString(in.State.Summary)
	b.WriteString("\n")
	if len(in.State.Values) > 0 {
		b.WriteString("VALUES:")
		for _, k := range domain.SortedKeys(in.State.Values) {
			fmt.Fprintf(&b, " %s=%.2f", k, in.State.Values[k])
		}
		b.WriteString("\n")
	}
	if len(in.Rules.Notes) > 0 {
		b.WriteString("INSTANCE RULES: ")
		b.WriteString(strings.Join(in.Rules.Notes, "; "))
		b.WriteString("\n")
	}
	if len(in.Control.Hard) > 0 {
		b.WriteString("HARD CONSTRAINTS (must obey):\n")
		for _, c := range in.Control.Hard {
			if c.IsActive(time.Now()) {
				fmt.Fprintf(&b, "- %s (deny actions=%v targets=%v tags=%v)\n", c.Text, c.DenyActions, c.DenyTargets, c.DenyTags)
			}
		}
	}
	if len(in.Hints) > 0 || len(in.Control.Hints) > 0 {
		b.WriteString("SOFT PREFERENCES (hints only):\n")
		for _, h := range append(append([]string{}, in.Control.Hints...), in.Hints...) {
			b.WriteString("- " + h + "\n")
		}
	}
	b.WriteString("LEGAL ACTIONS:\n")
	for i, a := range legal {
		fmt.Fprintf(&b, "%d. kind=%s target=%s risk=%.2f tags=%s\n", i+1, a.Kind, a.Target, a.Risk, strings.Join(a.Tags, ","))
	}
	b.WriteString("Reply JSON: {\"kind\":\"...\",\"target\":\"...\",\"reason\":\"...\",\"confidence\":0.0}")
	return b.String()
}

func fallbackWait() domain.Action {
	return domain.Action{
		Kind: "wait", Target: "self", Tags: []string{"wait", "safe"},
		Reason: "safe fallback", Risk: 0,
		Input: &domain.InputSpec{Type: "wait", DurationMS: 200},
	}
}

func bestLegalOrWait(legal []domain.Action) domain.Action {
	if len(legal) > 0 {
		return legal[0]
	}
	return fallbackWait()
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
