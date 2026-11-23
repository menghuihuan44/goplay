package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
)

// Manager owns the global store and one isolated store per game instance.
type Manager struct {
	mu          sync.RWMutex
	dir         string
	budgetKB    int
	global      *GlobalMemory
	instances   map[string]*InstanceMemory
	dirty       map[string]bool
	globalDirty bool
}

func Open(dir string, budgetKB int) (*Manager, error) {
	if dir == "" {
		return nil, fmt.Errorf("memory: empty data dir")
	}
	if err := os.MkdirAll(filepath.Join(dir, "instances"), 0o755); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, budgetKB: budgetKB, instances: map[string]*InstanceMemory{}, dirty: map[string]bool{}}
	g, err := loadGlobal(filepath.Join(dir, "global.json"))
	if err != nil {
		return nil, err
	}
	m.global = g
	return m, nil
}

func loadGlobal(path string) (*GlobalMemory, error) {
	g := NewGlobal()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return g, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return g, nil
	}
	if err := json.Unmarshal(b, g); err != nil {
		return nil, fmt.Errorf("load global memory: %w", err)
	}
	if g.Preferences == nil {
		g.Preferences = map[string]domain.Preference{}
	}
	return g, nil
}

func (m *Manager) Global() *GlobalMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.global
}

func (m *Manager) MarkGlobalDirty() {
	m.mu.Lock()
	m.globalDirty = true
	m.mu.Unlock()
}

// Instance returns (creating if needed) the isolated memory of one game.
func (m *Manager) Instance(id string) *InstanceMemory {
	m.mu.Lock()
	defer m.mu.Unlock()
	if im, ok := m.instances[id]; ok {
		return im
	}
	im := NewInstance(id, m.budgetKB)
	path := filepath.Join(m.dir, "instances", safeName(id), "memory.json")
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, im); err == nil {
			if im.BudgetKB <= 0 {
				im.BudgetKB = m.budgetKB
			}
			im.dir = filepath.Dir(path)
		}
	}
	im.dir = filepath.Join(m.dir, "instances", safeName(id))
	m.instances[id] = im
	return im
}

func safeName(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

func (m *Manager) SaveInstance(id string) error {
	m.mu.Lock()
	im, ok := m.instances[id]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	im.mu.Lock()
	dir := im.dir
	if dir == "" {
		dir = filepath.Join(m.dir, "instances", safeName(id))
		im.dir = dir
	}
	im.refreshStats()
	im.UpdatedAt = time.Now()
	snap := im.snapshotLocked()
	im.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "memory.json"), b)
}

func (m *Manager) SaveGlobal() error {
	m.mu.RLock()
	g := m.global
	m.mu.RUnlock()
	b, err := json.MarshalIndent(g.SnapshotCopy(), "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.dir, "global.json"), b)
}

func (m *Manager) SaveAll() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := m.SaveInstance(id); err != nil {
			return err
		}
	}
	if err := m.SaveGlobal(); err != nil {
		return err
	}
	m.mu.Lock()
	m.dirty = map[string]bool{}
	m.globalDirty = false
	m.mu.Unlock()
	return nil
}

func (m *Manager) DeleteInstance(id string) error {
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	return os.RemoveAll(filepath.Join(m.dir, "instances", safeName(id)))
}

func (m *Manager) ListInstances() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.instances))
	for id := range m.instances {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// AddGlobalPreference persists a cross-game preference and records a global
// memory entry so it is retrievable in later sessions.
func (m *Manager) AddGlobalPreference(p domain.Preference) error {
	g := m.Global()
	g.UpsertPreference(p)
	if p.Text != "" {
		_ = g.AddFact(Entry{
			Kind:       KindPreference,
			Text:       p.Text,
			Tags:       append([]string{"preference", "global"}, p.AvoidTags...),
			Importance: 0.85,
			Source:     "user",
		})
	}
	m.MarkGlobalDirty()
	return m.SaveGlobal()
}

func atomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
