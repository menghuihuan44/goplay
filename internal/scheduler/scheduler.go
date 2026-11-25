package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/memory"
)

type BoundaryKind string

const (
	BoundaryBeforeDecision BoundaryKind = "before_decision"
	BoundaryAfterAction    BoundaryKind = "after_action"
	BoundaryHumanTakeover  BoundaryKind = "human_takeover"
	BoundaryIdle           BoundaryKind = "idle"
	BoundaryUserMessage    BoundaryKind = "user_message"
)

type BoundaryResult struct {
	Kind      BoundaryKind   `json:"kind"`
	Intents   []Intent       `json:"intents"`
	Commands  []string       `json:"commands,omitempty"`
	Control   domain.Control `json:"control"`
	Processed int            `json:"processed"`
	At        time.Time      `json:"at"`
}

type ChatMessage struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	Role       string    `json:"role"` // user, assistant, system
	Text       string    `json:"text"`
	Kind       string    `json:"kind,omitempty"`
	At         time.Time `json:"at"`
}

type MetricsSink interface {
	Inc(name string, delta float64)
}

type Scheduler struct {
	mu          sync.Mutex
	instanceID  string
	gameName    string
	rules       domain.InstanceRules
	buffer      *Buffer
	classifier  Classifier
	mgr         *memory.Manager
	metrics     MetricsSink
	constraints []domain.Constraint
	instPrefs   map[string]domain.Preference
	chat        []ChatMessage
	path        string
	lastControl domain.Control
	lastAdapt   []domain.Adaptation
	directives  []Directive
}

func New(instanceID, gameName string, rules domain.InstanceRules, mgr *memory.Manager, classifier Classifier, sink MetricsSink, persistPath string) *Scheduler {
	if classifier == nil {
		classifier = RuleClassifier{}
	}
	s := &Scheduler{
		instanceID: instanceID,
		gameName:   gameName,
		rules:      rules,
		buffer:     NewBuffer(128),
		classifier: classifier,
		mgr:        mgr,
		metrics:    sink,
		instPrefs:  map[string]domain.Preference{},
		path:       persistPath,
	}
	s.load()
	return s
}

func (s *Scheduler) Submit(msg Message) {
	msg.InstanceID = s.instanceID
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	s.buffer.Submit(msg)
	// Acknowledge in the transcript immediately so the user can see that the
	// instruction is queued even when the instance is stopped. Classification,
	// constraints and preference writes still happen at the next boundary.
	s.AddChat(ChatMessage{ID: msg.ID, Role: "user", Text: msg.Text, Kind: "queued", At: msg.At})
	s.AddChat(ChatMessage{ID: msg.ID + "-r", Role: "assistant", Text: "已收到，将在下一个决策周期生效。", Kind: "queued"})
}

func (s *Scheduler) Pending() int { return s.buffer.Len() }

// TakeDirectives removes and returns the pending one-shot instructions. The
// engine calls this at the same event boundary as constraints and preferences,
// so a typed instruction reaches the very next decision without waiting for the
// LLM or a new episode.
func (s *Scheduler) TakeDirectives() []Directive {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Directive, 0, len(s.directives))
	kept := s.directives[:0]
	for _, d := range s.directives {
		if now.After(d.Expires) {
			continue
		}
		out = append(out, d)
	}
	s.directives = kept
	return out
}

func (s *Scheduler) InstanceID() string { return s.instanceID }

func (s *Scheduler) AddChat(m ChatMessage) {
	m.InstanceID = s.instanceID
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s.mu.Lock()
	for i := range s.chat {
		if s.chat[i].ID == m.ID {
			s.chat[i] = m
			s.mu.Unlock()
			return
		}
	}
	s.chat = append(s.chat, m)
	if len(s.chat) > 200 {
		s.chat = s.chat[len(s.chat)-200:]
	}
	s.mu.Unlock()
}

func (s *Scheduler) ChatLog() []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ChatMessage, len(s.chat))
	copy(out, s.chat)
	return out
}

// Boundary drains the instruction buffer and applies each intent. This is the
// only place where user input can change control, which is what makes the
// "next event boundary" guarantee true.
func (s *Scheduler) Boundary(kind BoundaryKind, cc ClassifyContext) BoundaryResult {
	msgs := s.buffer.Drain()
	if cc.InstanceID == "" {
		cc.InstanceID = s.instanceID
	}
	if cc.GameName == "" {
		cc.GameName = s.gameName
	}
	cc.Now = time.Now()
	res := BoundaryResult{Kind: kind, Processed: len(msgs), At: cc.Now}
	changed := false
	for _, m := range msgs {
		intent := s.classifier.Classify(m, cc)
		res.Intents = append(res.Intents, intent)
		s.AddChat(ChatMessage{ID: m.ID, Role: "user", Text: m.Text, Kind: string(intent.Kind), At: m.At})
		switch intent.Kind {
		case IntentConstraint:
			if intent.Constraint != nil {
				s.mu.Lock()
				s.constraints = append(s.constraints, *intent.Constraint)
				s.mu.Unlock()
				changed = true
				if s.metrics != nil {
					s.metrics.Inc("constraints_applied_total", 1)
				}
			}
		case IntentPreference:
			if intent.Preference != nil {
				p := *intent.Preference
				if intent.Scope == "global" && s.mgr != nil {
					if err := s.mgr.AddGlobalPreference(p); err != nil {
						intent.Reply = "偏好写入失败：" + err.Error()
					}
				} else {
					if p.ID == "" {
						p.ID = fmt.Sprintf("ip-%d", time.Now().UnixNano())
					}
					s.mu.Lock()
					s.instPrefs[p.ID] = p
					s.mu.Unlock()
				}
				changed = true
				if s.metrics != nil {
					s.metrics.Inc("preferences_applied_total", 1)
				}
			}
		case IntentCommand:
			res.Commands = append(res.Commands, intent.Command)
		case IntentDirective:
			if intent.Directive != nil {
				s.mu.Lock()
				s.directives = append(s.directives, *intent.Directive)
				if len(s.directives) > 8 {
					s.directives = s.directives[len(s.directives)-8:]
				}
				s.mu.Unlock()
				if s.metrics != nil {
					s.metrics.Inc("directives_applied_total", 1)
				}
			}
		}
		if intent.Reply != "" {
			s.AddChat(ChatMessage{ID: m.ID + "-r", Role: "assistant", Text: intent.Reply, Kind: string(intent.Kind)})
		}
	}
	if changed {
		s.RefreshControl()
		s.save()
	}
	s.mu.Lock()
	res.Control = s.lastControl
	s.mu.Unlock()
	if s.metrics != nil {
		s.metrics.Inc("boundary_events_total", 1)
		if len(msgs) > 0 {
			s.metrics.Inc("user_interventions_per_boundary", float64(len(msgs)))
		}
	}
	return res
}

// RefreshControl merges instance rules, hard constraints and adapted global +
// instance preferences into the control used by the next decision.
func (s *Scheduler) RefreshControl() domain.Control {
	now := time.Now()
	s.mu.Lock()
	globalPrefs := []domain.Preference{}
	if s.mgr != nil {
		globalPrefs = s.mgr.Global().PreferenceList()
	}
	prefs := make([]domain.Preference, 0, len(globalPrefs)+len(s.instPrefs))
	for _, p := range globalPrefs {
		if preferenceAppliesTo(p, s.gameName) {
			prefs = append(prefs, p)
		}
	}
	for _, p := range mapPrefs(s.instPrefs) {
		if preferenceAppliesTo(p, s.gameName) {
			prefs = append(prefs, p)
		}
	}
	rules := s.rules
	hard := make([]domain.Constraint, 0, len(s.constraints))
	for _, c := range s.constraints {
		if c.IsActive(now) {
			hard = append(hard, c)
		}
	}
	s.mu.Unlock()
	ctl := Adapt(prefs, rules, now)
	ctl.Hard = hard
	s.mu.Lock()
	s.lastControl = ctl
	s.lastAdapt = ctl.Adaptations
	s.mu.Unlock()
	return ctl
}

func mapPrefs(m map[string]domain.Preference) []domain.Preference {
	out := make([]domain.Preference, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}

// preferenceAppliesTo keeps an unscoped preference global, while a preference
// explicitly scoped to other games never leaks into this instance's weights.
func preferenceAppliesTo(p domain.Preference, gameName string) bool {
	if len(p.AppliesTo) == 0 {
		return true
	}
	gameName = strings.ToLower(strings.TrimSpace(gameName))
	for _, want := range p.AppliesTo {
		if strings.EqualFold(strings.TrimSpace(want), gameName) {
			return true
		}
	}
	return false
}

func (s *Scheduler) Control() domain.Control {
	s.mu.Lock()
	ctl := s.lastControl
	s.mu.Unlock()
	if ctl.Weights == nil {
		return s.RefreshControl()
	}
	return ctl
}

func (s *Scheduler) Constraints() []domain.Constraint {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Constraint
	for _, c := range s.constraints {
		if c.IsActive(now) {
			out = append(out, c)
		}
	}
	return out
}

// AddConstraint is used by the API / safety settings, not by chat.
func (s *Scheduler) AddConstraint(c domain.Constraint) {
	if c.InstanceID == "" {
		c.InstanceID = s.instanceID
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.Active = true
	s.mu.Lock()
	s.constraints = append(s.constraints, c)
	s.mu.Unlock()
	s.RefreshControl()
	s.save()
}

func (s *Scheduler) SetRules(r domain.InstanceRules) {
	s.mu.Lock()
	s.rules = r
	s.mu.Unlock()
	s.RefreshControl()
}

func (s *Scheduler) Rules() domain.InstanceRules {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rules
}

func (s *Scheduler) AddPreference(p domain.Preference) {
	if p.ID == "" {
		p.ID = fmt.Sprintf("ip-%d", time.Now().UnixNano())
	}
	p.UpdatedAt = time.Now()
	s.mu.Lock()
	s.instPrefs[p.ID] = p
	s.mu.Unlock()
	s.RefreshControl()
	s.save()
}

type persisted struct {
	Constraints   []domain.Constraint          `json:"constraints"`
	InstancePrefs map[string]domain.Preference `json:"instance_preferences"`
	Chat          []ChatMessage                `json:"chat"`
}

func (s *Scheduler) save() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	p := persisted{Constraints: s.constraints, InstancePrefs: s.instPrefs, Chat: s.chat}
	s.mu.Unlock()
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *Scheduler) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return
	}
	s.constraints = p.Constraints
	if p.InstancePrefs != nil {
		s.instPrefs = p.InstancePrefs
	}
	if len(p.Chat) > 0 {
		s.chat = p.Chat
	}
	s.RefreshControl()
}
