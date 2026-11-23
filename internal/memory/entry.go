package memory

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
)

type Tier string

const (
	TierWorking Tier = "working"
	TierSummary Tier = "summary"
	TierFact    Tier = "fact"
)

type Kind string

const (
	KindEvent       Kind = "event"
	KindObservation Kind = "observation"
	KindDecision    Kind = "decision"
	KindSummary     Kind = "summary"
	KindFact        Kind = "fact"
	KindPreference  Kind = "preference"
	KindStyle       Kind = "style"
	KindSafety      Kind = "safety"
	KindInsight     Kind = "insight"
)

// Entry is one memory item. Text is always present so the item stays
// readable after compression. Values keep numeric game state that must be
// recalled verbatim.
type Entry struct {
	ID         string             `json:"id"`
	Tier       Tier               `json:"tier"`
	Kind       Kind               `json:"kind"`
	Text       string             `json:"text"`
	Tags       []string           `json:"tags,omitempty"`
	Values     map[string]float64 `json:"values,omitempty"`
	GameID     string             `json:"game_id,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	LastUsedAt time.Time          `json:"last_used_at,omitempty"`
	Importance float64            `json:"importance"`
	Accesses   int                `json:"accesses"`
	Tokens     int                `json:"tokens"`
	Source     string             `json:"source,omitempty"`
}

func (e *Entry) Normalize(now time.Time) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.Tokens <= 0 {
		e.Tokens = EstimateTokens(e.Text)
	}
	if e.Importance < 0 {
		e.Importance = 0
	}
	if e.Importance > 1 {
		e.Importance = 1
	}
	if e.ID == "" {
		h := fnv.New64a()
		fmt.Fprintf(h, "%s|%s|%s|%d", e.Kind, e.Text, strings.Join(e.Tags, ","), e.CreatedAt.UnixNano())
		e.ID = fmt.Sprintf("m%x", h.Sum64())
	}
	if e.LastUsedAt.IsZero() {
		e.LastUsedAt = e.CreatedAt
	}
}

func EstimateTokens(s string) int {
	n := 0
	for range s {
		n++
	}
	t := n / 4
	if t < 1 {
		t = 1
	}
	return t
}

func (e Entry) Bytes() int {
	n := len(e.Text) + 48
	for _, t := range e.Tags {
		n += len(t) + 2
	}
	for k := range e.Values {
		n += len(k) + 8
	}
	return n
}

// retentionScore is the budget-eviction priority. High importance, frequent
// access and recency survive; low-value chatter is dropped first.
func (e Entry) retentionScore(now time.Time) float64 {
	ageHours := now.Sub(e.CreatedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	rec := 1.0 / (1.0 + ageHours/72.0)
	acc := float64(e.Accesses)
	if acc > 8 {
		acc = 8
	}
	tier := 0.0
	switch e.Tier {
	case TierFact:
		tier = 0.35
	case TierSummary:
		tier = 0.2
	}
	return e.Importance*1.0 + rec*0.3 + acc*0.04 + tier
}

func (e Entry) clone() Entry {
	c := e
	c.Tags = append([]string(nil), e.Tags...)
	if e.Values != nil {
		c.Values = make(map[string]float64, len(e.Values))
		for k, v := range e.Values {
			c.Values[k] = v
		}
	}
	return c
}

type Stats struct {
	Added       int64 `json:"added"`
	Compactions int64 `json:"compactions"`
	Evictions   int64 `json:"evictions"`
	Recalls     int64 `json:"recalls"`
	Bytes       int   `json:"bytes"`
	Working     int   `json:"working"`
	Summaries   int   `json:"summaries"`
	Facts       int   `json:"facts"`
}

// InstanceMemory is the per-game memory: a short working set, a compressed
// middle tier, and a retrievable long-term fact tier.
type InstanceMemory struct {
	mu        sync.Mutex
	GameID    string    `json:"game_id"`
	BudgetKB  int       `json:"budget_kb"`
	Working   []Entry   `json:"working"`
	Summaries []Entry   `json:"summaries"`
	Facts     []Entry   `json:"facts"`
	Stats     Stats     `json:"stats"`
	UpdatedAt time.Time `json:"updated_at"`
	dir       string
}

const (
	defaultBudgetKB     = 256
	maxEntryTextRunes   = 1200
	compactBatch        = 8
	recallCandidatePool = 200
)

func NewInstance(gameID string, budgetKB int) *InstanceMemory {
	if budgetKB <= 0 {
		budgetKB = defaultBudgetKB
	}
	return &InstanceMemory{GameID: gameID, BudgetKB: budgetKB, UpdatedAt: time.Now()}
}

func (m *InstanceMemory) Add(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	e.Normalize(now)
	e.GameID = m.GameID
	if len(e.Text) > maxEntryTextRunes*4 {
		e.Text = e.Text[:maxEntryTextRunes*4] + "..."
		e.Tokens = EstimateTokens(e.Text)
	}
	if e.Tier == "" || e.Kind == KindEvent || e.Kind == KindObservation || e.Kind == KindDecision {
		e.Tier = TierWorking
	}
	switch e.Tier {
	case TierWorking:
		m.Working = append(m.Working, e)
	case TierSummary:
		m.Summaries = append(m.Summaries, e)
	case TierFact:
		m.Facts = append(m.Facts, e)
	default:
		e.Tier = TierWorking
		m.Working = append(m.Working, e)
	}
	m.Stats.Added++
	m.UpdatedAt = now
	if len(m.Working) > 48 || m.usageBytesLocked() > m.BudgetKB*1024 {
		m.compactLocked(now)
	}
}

func (m *InstanceMemory) usageBytesLocked() int {
	n := 0
	for _, e := range m.Working {
		n += e.Bytes()
	}
	for _, e := range m.Summaries {
		n += e.Bytes()
	}
	for _, e := range m.Facts {
		n += e.Bytes()
	}
	return n
}

// Compact folds the oldest working entries into deterministic summaries and
// enforces the storage budget. The summarizer preserves numbers, outcomes and
// tags, so exact values remain recallable after compression.
func (m *InstanceMemory) Compact(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactLocked(now)
}

func (m *InstanceMemory) compactLocked(now time.Time) {
	m.Stats.Compactions++
	for len(m.Working) > 24 {
		batch := m.Working[:compactBatch]
		m.Working = append([]Entry(nil), m.Working[compactBatch:]...)
		m.Summaries = append(m.Summaries, summarizeBatch(batch, m.GameID, now))
	}
	// Too many summaries: fold the oldest half into one higher-level summary.
	for len(m.Summaries) > 40 {
		n := len(m.Summaries) / 2
		merged := summarizeBatch(m.Summaries[:n], m.GameID, now)
		merged.Importance = maxImportance(m.Summaries[:n])
		merged.Text = "LONG-TERM SUMMARY: " + merged.Text
		m.Summaries = append([]Entry{merged}, m.Summaries[n:]...)
	}
	// Budget enforcement: drop lowest-retention entries, but never drop facts
	// with importance >= 0.85 unless there is literally nothing else.
	for m.usageBytesLocked() > m.BudgetKB*1024 {
		idx, tier := m.lowestRetention()
		if idx < 0 {
			break
		}
		switch tier {
		case TierWorking:
			m.Working = append(m.Working[:idx], m.Working[idx+1:]...)
		case TierSummary:
			m.Summaries = append(m.Summaries[:idx], m.Summaries[idx+1:]...)
		case TierFact:
			m.Facts = append(m.Facts[:idx], m.Facts[idx+1:]...)
		}
		m.Stats.Evictions++
	}
	m.refreshStats()
}

func (m *InstanceMemory) refreshStats() {
	m.Stats.Bytes = m.usageBytesLocked()
	m.Stats.Working = len(m.Working)
	m.Stats.Summaries = len(m.Summaries)
	m.Stats.Facts = len(m.Facts)
}

func (m *InstanceMemory) lowestRetention() (int, Tier) {
	now := time.Now()
	best := 1e9
	bestIdx := -1
	var bestTier Tier
	consider := func(entries []Entry, tier Tier, protected bool) {
		for i, e := range entries {
			if protected && e.Importance >= 0.85 {
				continue
			}
			s := e.retentionScore(now)
			if s < best {
				best = s
				bestIdx = i
				bestTier = tier
			}
		}
	}
	consider(m.Working, TierWorking, false)
	consider(m.Summaries, TierSummary, false)
	consider(m.Facts, TierFact, true)
	if bestIdx < 0 {
		// Everything left is a protected fact; allow dropping the least
		// important one only if the budget is still exceeded.
		for i, e := range m.Facts {
			s := e.retentionScore(now)
			if s < best {
				best = s
				bestIdx = i
				bestTier = TierFact
			}
		}
	}
	return bestIdx, bestTier
}

func maxImportance(es []Entry) float64 {
	v := 0.0
	for _, e := range es {
		if e.Importance > v {
			v = e.Importance
		}
	}
	return v
}

func (m *InstanceMemory) Promote(index int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index < 0 || index >= len(m.Working) {
		return
	}
	e := m.Working[index]
	e.Tier = TierFact
	e.Kind = KindFact
	e.Importance = maxf(e.Importance, 0.8)
	m.Working = append(m.Working[:index], m.Working[index+1:]...)
	m.Facts = append(m.Facts, e)
	m.refreshStats()
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Recent returns the newest n entries across all tiers (working first).
func (m *InstanceMemory) Recent(n int) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, 0, n)
	for i := len(m.Facts) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, m.Facts[i].clone())
	}
	for i := len(m.Summaries) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, m.Summaries[i].clone())
	}
	for i := len(m.Working) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, m.Working[i].clone())
	}
	return out
}

func (m *InstanceMemory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Working) + len(m.Summaries) + len(m.Facts)
}

func (m *InstanceMemory) Bytes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usageBytesLocked()
}

// StatsSnapshot returns a copy safe for JSON.
func (m *InstanceMemory) StatsSnapshot() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.Stats
	s.Bytes = m.usageBytesLocked()
	s.Working = len(m.Working)
	s.Summaries = len(m.Summaries)
	s.Facts = len(m.Facts)
	return s
}

// SnapshotCopy returns a deep copy under the lock so persistence never races
// with the running decision loop.
func (m *InstanceMemory) SnapshotCopy() *InstanceMemory {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *InstanceMemory) snapshotLocked() *InstanceMemory {
	c := &InstanceMemory{
		GameID:    m.GameID,
		BudgetKB:  m.BudgetKB,
		Stats:     m.Stats,
		UpdatedAt: m.UpdatedAt,
		dir:       m.dir,
	}
	c.Stats.Bytes = m.usageBytesLocked()
	c.Stats.Working = len(m.Working)
	c.Stats.Summaries = len(m.Summaries)
	c.Stats.Facts = len(m.Facts)
	c.Working = cloneEntries(m.Working)
	c.Summaries = cloneEntries(m.Summaries)
	c.Facts = cloneEntries(m.Facts)
	return c
}

func cloneEntries(in []Entry) []Entry {
	if in == nil {
		return nil
	}
	out := make([]Entry, len(in))
	for i := range in {
		out[i] = in[i].clone()
	}
	return out
}

// GlobalMemory holds only cross-game material: user style/preferences,
// safety policy and abstract insights. It rejects concrete game state.
type GlobalMemory struct {
	mu          sync.Mutex
	Preferences map[string]domain.Preference `json:"preferences"`
	Facts       []Entry                      `json:"facts"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

func NewGlobal() *GlobalMemory {
	return &GlobalMemory{Preferences: map[string]domain.Preference{}}
}

func (g *GlobalMemory) Validate(entry Entry) error {
	if entry.GameID != "" {
		return fmt.Errorf("global memory must not contain game-scoped state (game_id=%q)", entry.GameID)
	}
	switch entry.Kind {
	case KindPreference, KindStyle, KindSafety, KindInsight, KindFact:
		return nil
	default:
		return fmt.Errorf("global memory rejects kind %q: only preferences/style/safety/insights are allowed", entry.Kind)
	}
}

func (g *GlobalMemory) AddFact(e Entry) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.Validate(e); err != nil {
		return err
	}
	e.Normalize(time.Now())
	e.Tier = TierFact
	e.GameID = ""
	g.Facts = append(g.Facts, e)
	if len(g.Facts) > 256 {
		g.Facts = g.Facts[len(g.Facts)-256:]
	}
	g.UpdatedAt = time.Now()
	return nil
}

func (g *GlobalMemory) UpsertPreference(p domain.Preference) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if p.ID == "" {
		h := fnv.New64a()
		fmt.Fprintf(h, "%s", p.Text)
		p.ID = fmt.Sprintf("p%x", h.Sum64())
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	g.Preferences[p.ID] = p
	g.UpdatedAt = now
}

func (g *GlobalMemory) RemovePreference(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.Preferences[id]; !ok {
		return false
	}
	delete(g.Preferences, id)
	g.UpdatedAt = time.Now()
	return true
}

func (g *GlobalMemory) PreferenceList() []domain.Preference {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]domain.Preference, 0, len(g.Preferences))
	for _, p := range g.Preferences {
		out = append(out, p)
	}
	return out
}

// SnapshotCopy returns a deep copy under the lock for safe persistence.
func (g *GlobalMemory) SnapshotCopy() *GlobalMemory {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := &GlobalMemory{
		Preferences: make(map[string]domain.Preference, len(g.Preferences)),
		Facts:       cloneEntries(g.Facts),
		UpdatedAt:   g.UpdatedAt,
	}
	for k, v := range g.Preferences {
		c.Preferences[k] = v
	}
	return c
}

func (g *GlobalMemory) Recall(query string, k int) []Scored {
	g.mu.Lock()
	defer g.mu.Unlock()
	cands := make([]Entry, 0, len(g.Facts))
	for _, f := range g.Facts {
		cands = append(cands, f)
	}
	return recall(query, cands, k, time.Now())
}
