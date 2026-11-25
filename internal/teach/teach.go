// Package teach learns reusable skills from human demonstrations.
//
// It is deliberately game-agnostic: it records what was on screen before and
// after a real key/click, stores a compact visual signature plus thumbnails, and
// later offers the same action when a similar screen appears. No game-specific
// keys or pixel rules live here.
package teach

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"goplay/internal/action"
	"goplay/internal/domain"
)

const (
	sigW      = 24
	sigH      = 14
	maxDemos  = 40
	maxSteps  = 80
	thumbEdge = 360
	// MatchThreshold is the minimum visual similarity for reusing a demo step.
	// Dynamic game frames never match bit-for-bit; measured same-scene demos
	// score about 0.83 while a different screen scores about 0.59. 0.80 keeps
	// same-scene imitation while rejecting unrelated screens.
	MatchThreshold = 0.80
	// HintCooldown prevents the same step from firing every tick when the screen
	// is static and the learned action did not change it.
	HintCooldown = 1200 * time.Millisecond
)

// Step is one demonstrated input and the screen transition around it.
type Step struct {
	ID          string        `json:"id"`
	DemoID      string        `json:"demo_id"`
	BeforeImage string        `json:"before_image,omitempty"`
	AfterImage  string        `json:"after_image,omitempty"`
	Action      domain.Action `json:"action"`
	Note        string        `json:"note,omitempty"`
	Intent      string        `json:"intent,omitempty"`
	BeforeSig   []byte        `json:"before_sig,omitempty"`
	AfterSig    []byte        `json:"after_sig,omitempty"`
	Similarity  float64       `json:"similarity,omitempty"`
	At          time.Time     `json:"at"`
}

// Demo is one continuous human takeover (or a labelled demonstration session).
type Demo struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Steps      []Step    `json:"steps"`
	Summary    string    `json:"summary,omitempty"`
	Source     string    `json:"source,omitempty"`
	// Dir is the store root used to load thumbnails for reflection. It is not
	// persisted or exposed over HTTP.
	Dir string `json:"-"`
}

// Status is embedded in the instance snapshot so the UI can show what the AI is
// learning while the human plays.
type Status struct {
	Enabled   bool      `json:"enabled"`
	Recording bool      `json:"recording"`
	Steps     int       `json:"steps"`
	Demos     int       `json:"demos"`
	Hints     int       `json:"hints"`
	Uses      int       `json:"uses"`
	HintSim   float64   `json:"hint_similarity,omitempty"`
	LastNote  string    `json:"last_note,omitempty"`
	LastAt    time.Time `json:"last_at,omitempty"`
}

// Info is the API-facing view of the teaching store.
type Info struct {
	Status Status `json:"status"`
	Demos  []Demo `json:"demos"`
}

// Hint is a learned action whose before-screen resembles the current frame.
type Hint struct {
	Action     domain.Action `json:"action"`
	Similarity float64       `json:"similarity"`
	StepID     string        `json:"step_id"`
	DemoID     string        `json:"demo_id"`
	Note       string        `json:"note,omitempty"`
	Intent     string        `json:"intent,omitempty"`
}

// Reflector turns a finished demo into a short natural-language explanation of
// what the user was trying to do. The manager wires it to the analysis role;
// without a model the heuristic summary is still useful.
type Reflector interface {
	Reflect(ctx context.Context, demo Demo) (string, error)
}

// Store persists demonstrations under one instance directory.
type Store struct {
	dir   string
	mu    sync.Mutex
	demos []Demo
}

// Open creates or loads a teaching store. The directory is created on demand.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("teach: create %s: %w", dir, err)
	}
	s := &Store{dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, "demos.json"))
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &s.demos)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("teach: read demos: %w", err)
	}
	return s, nil
}

// Dir returns the store root.
func (s *Store) Dir() string { return s.dir }

// Snapshot returns a copy of the demos, newest last.
func (s *Store) Snapshot() []Demo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Demo, len(s.demos))
	for i, d := range s.demos {
		d.Steps = append([]Step(nil), d.Steps...)
		out[i] = d
	}
	return out
}

// Add appends a demo, evicts the oldest beyond maxDemos, and persists.
func (s *Store) Add(d Demo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demos = append(s.demos, d)
	for len(s.demos) > maxDemos {
		old := s.demos[0]
		s.demos = s.demos[1:]
		_ = os.RemoveAll(filepath.Join(s.dir, "frames", old.ID))
	}
	return s.saveLocked()
}

// UpdateSummary replaces the summary of one demo.
func (s *Store) UpdateSummary(id, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.demos {
		if s.demos[i].ID == id {
			s.demos[i].Summary = summary
			return s.saveLocked()
		}
	}
	return nil
}

// Clear removes every demo and thumbnail.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demos = nil
	if err := os.RemoveAll(filepath.Join(s.dir, "frames")); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dir, "demos.json"))
	return nil
}

// ImagePath resolves a stored relative image path safely inside the store.
func (s *Store) ImagePath(rel string) (string, error) {
	if rel == "" || strings.Contains(rel, "..") {
		return "", fmt.Errorf("teach: invalid image path")
	}
	full := filepath.Join(s.dir, filepath.FromSlash(rel))
	clean := filepath.Clean(full)
	root := filepath.Clean(s.dir)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("teach: image path escapes store")
	}
	return clean, nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.demos, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "demos.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "demos.json"))
}

// Recorder watches a human takeover and turns it into reusable demos.
type learnedStep struct {
	step Step
}

type Recorder struct {
	mu         sync.Mutex
	store      *Store
	instanceID string
	enabled    bool
	active     bool
	demo       *Demo
	pending    []Step
	prevFrame  *domain.Frame
	lastFrame  *domain.Frame
	lastAt     time.Time
	lastHintAt time.Time
	lastHintID string
	hints      int
	uses       int
	hintSim    float64
	reflector  Reflector

	// entries is an in-memory index of learned before-signatures. Hint runs on
	// every decision; scanning the persisted JSON store and copying every step
	// there would be wasteful once a user has dozens of demos.
	entries    []learnedStep
	totalDemos int
	totalSteps int
	latestNote string
	latestAt   time.Time
}

// NewRecorder returns a recorder for one instance. Learning is enabled by
// default: a user's takeover should teach the agent without extra setup.
func NewRecorder(store *Store, instanceID string) *Recorder {
	r := &Recorder{store: store, instanceID: instanceID, enabled: true}
	r.rebuildIndexLocked()
	return r
}

func (r *Recorder) rebuildIndexLocked() {
	r.entries = nil
	r.totalDemos = 0
	r.totalSteps = 0
	r.latestNote = ""
	r.latestAt = time.Time{}
	for _, d := range r.store.Snapshot() {
		r.totalDemos++
		r.totalSteps += len(d.Steps)
		for _, st := range d.Steps {
			r.entries = append(r.entries, learnedStep{step: st})
		}
		if d.Summary != "" {
			r.latestNote = d.Summary
		} else if len(d.Steps) > 0 {
			r.latestNote = d.Steps[len(d.Steps)-1].Note
		}
		if d.EndedAt.After(r.latestAt) {
			r.latestAt = d.EndedAt
		}
	}
}

// SetReflector installs the optional model-backed explanation step.
func (r *Recorder) SetReflector(v Reflector) {
	r.mu.Lock()
	r.reflector = v
	r.mu.Unlock()
}

// SetEnabled turns learning on or off. Turning it off ends an active demo.
func (r *Recorder) SetEnabled(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !on {
		r.endLocked(time.Now())
	}
	r.enabled = on
}

// Observe consumes one human-control tick. When active is false it ends the
// current demo. Frames are copied so the caller can reuse its capture buffer.
func (r *Recorder) Observe(frame domain.Frame, events []action.InputEvent, active bool, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled {
		return
	}
	if !active {
		r.endLocked(now)
		return
	}
	if !r.active {
		r.demo = &Demo{
			ID:         newID("demo"),
			InstanceID: r.instanceID,
			StartedAt:  now,
			Source:     "human-takeover",
			Dir:        r.store.Dir(),
		}
		r.active = true
		r.prevFrame = nil
	}
	if r.demo == nil {
		return
	}

	after := copyFrame(frame)
	r.finalizeLocked(after, now)

	if len(events) > 0 && len(r.demo.Steps)+len(r.pending) < maxSteps {
		before := after
		if r.prevFrame != nil {
			before = *r.prevFrame
		}
		for _, ev := range events {
			act, ok := ActionFromEvent(ev)
			if !ok {
				continue
			}
			step := Step{
				ID:        newID("step"),
				DemoID:    r.demo.ID,
				Action:    act,
				Intent:    IntentFor(ev),
				Note:      NoteFor(ev),
				BeforeSig: Signature(before),
				At:        ev.At,
			}
			if rel, err := r.saveThumb(r.demo.ID, step.ID+"-before.jpg", before); err == nil {
				step.BeforeImage = rel
			}
			r.pending = append(r.pending, step)
			if len(r.demo.Steps)+len(r.pending) >= maxSteps {
				break
			}
		}
	}

	r.prevFrame = &after
	r.lastFrame = &after
	r.lastAt = now
}

// End closes the active demo; the engine calls it when the human releases
// control or the instance stops.
func (r *Recorder) End(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endLocked(now)
}

func (r *Recorder) endLocked(now time.Time) {
	if !r.active {
		return
	}
	if r.lastFrame != nil {
		r.finalizeLocked(*r.lastFrame, now)
	}
	if r.demo != nil && len(r.demo.Steps) > 0 {
		r.demo.EndedAt = now
		r.demo.Summary = summarize(r.demo)
		demo := *r.demo
		demo.Steps = append([]Step(nil), r.demo.Steps...)
		if err := r.store.Add(demo); err == nil {
			r.totalDemos++
			r.totalSteps += len(demo.Steps)
			for _, st := range demo.Steps {
				r.entries = append(r.entries, learnedStep{step: st})
			}
			r.latestNote = demo.Summary
			r.latestAt = demo.EndedAt
			r.startReflect(r.reflector, demo)
		}
	}
	r.active = false
	r.demo = nil
	r.pending = nil
	r.prevFrame = nil
	r.lastFrame = nil
}

func (r *Recorder) finalizeLocked(after domain.Frame, now time.Time) {
	if len(r.pending) == 0 {
		return
	}
	sig := Signature(after)
	for i := range r.pending {
		st := &r.pending[i]
		st.AfterSig = sig
		if rel, err := r.saveThumb(st.DemoID, st.ID+"-after.jpg", after); err == nil {
			st.AfterImage = rel
		}
		delta := 1 - Similarity(st.BeforeSig, st.AfterSig)
		if delta < 0 {
			delta = 0
		}
		st.Note = fmt.Sprintf("%s；执行后画面变化约 %.0f%%。", st.Note, delta*100)
		if delta < 0.01 {
			st.Note += "这一步几乎没有改变画面，可能是菜单确认或持续按键。"
		}
		if r.demo != nil {
			r.demo.Steps = append(r.demo.Steps, *st)
		}
	}
	r.pending = nil
	_ = now
}

func (r *Recorder) saveThumb(demoID, name string, frame domain.Frame) (string, error) {
	rel := path.Join("frames", demoID, name)
	full, err := r.store.ImagePath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	raw, err := thumbnailJPEG(frame)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

// Hint returns the best matching demonstrated action for the current frame.
func (r *Recorder) Hint(frame domain.Frame) (Hint, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled {
		return Hint{}, false
	}
	sig := Signature(frame)
	now := time.Now()
	var best Step
	bestSim := 0.0
	for _, entry := range r.entries {
		st := entry.step
		if len(st.BeforeSig) != len(sig) || st.Action.Input == nil {
			continue
		}
		sim := Similarity(sig, st.BeforeSig)
		if sim > bestSim {
			bestSim = sim
			best = st
		}
	}
	if bestSim < MatchThreshold || best.ID == "" {
		return Hint{}, false
	}
	if best.ID == r.lastHintID && now.Sub(r.lastHintAt) < HintCooldown {
		return Hint{}, false
	}
	r.lastHintID = best.ID
	r.lastHintAt = now
	r.hints++
	r.hintSim = bestSim
	return Hint{
		Action:     best.Action,
		Similarity: bestSim,
		StepID:     best.ID,
		DemoID:     best.DemoID,
		Note:       best.Note,
		Intent:     best.Intent,
	}, true
}

// MarkUsed records that the policy actually executed a hinted demo action,
// as opposed to merely finding a similar frame.
func (r *Recorder) MarkUsed() {
	r.mu.Lock()
	r.uses++
	r.mu.Unlock()
}

// Status reports what the recorder currently knows.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	enabled, active := r.enabled, r.active
	var lastNote string
	var lastAt time.Time
	if r.demo != nil && len(r.demo.Steps) > 0 {
		lastNote = r.demo.Steps[len(r.demo.Steps)-1].Note
		lastAt = r.demo.Steps[len(r.demo.Steps)-1].At
	}
	pending := len(r.pending)
	steps := r.totalSteps + pending
	demos := r.totalDemos
	if r.latestNote != "" {
		lastNote = r.latestNote
	}
	if r.latestAt.After(lastAt) {
		lastAt = r.latestAt
	}
	hints, uses, hintSim := r.hints, r.uses, r.hintSim
	r.mu.Unlock()
	return Status{
		Enabled:   enabled,
		Recording: active,
		Steps:     steps,
		Demos:     demos,
		Hints:     hints,
		Uses:      uses,
		HintSim:   hintSim,
		LastNote:  lastNote,
		LastAt:    lastAt,
	}
}

// Info returns the status plus demo history for the API/UI.
func (r *Recorder) Info() Info {
	status := r.Status()
	demos := r.store.Snapshot()
	sort.SliceStable(demos, func(i, j int) bool { return demos[i].StartedAt.Before(demos[j].StartedAt) })
	return Info{Status: status, Demos: demos}
}

// Clear deletes every learned demo and thumbnail.
func (r *Recorder) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endLocked(time.Now())
	if err := r.store.Clear(); err != nil {
		return err
	}
	r.entries = nil
	r.totalDemos = 0
	r.totalSteps = 0
	r.latestNote = ""
	r.latestAt = time.Time{}
	r.lastHintID = ""
	r.lastHintAt = time.Time{}
	return nil
}

func (r *Recorder) startReflect(ref Reflector, demo Demo) {
	if ref == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		text, err := ref.Reflect(ctx, demo)
		text = strings.TrimSpace(text)
		if err == nil && text != "" {
			_ = r.store.UpdateSummary(demo.ID, text)
		}
	}()
}

// Signature builds a compact RGB block signature. It is intentionally cheap and
// tolerant of small animation: each block is averaged.
func Signature(f domain.Frame) []byte {
	if f.Width <= 0 || f.Height <= 0 || len(f.RGBA) < f.Width*f.Height*4 {
		return nil
	}
	out := make([]byte, 0, sigW*sigH*3)
	for by := 0; by < sigH; by++ {
		y0 := by * f.Height / sigH
		y1 := (by + 1) * f.Height / sigH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for bx := 0; bx < sigW; bx++ {
			x0 := bx * f.Width / sigW
			x1 := (bx + 1) * f.Width / sigW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb, n int
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					i := (y*f.Width + x) * 4
					if i+3 >= len(f.RGBA) {
						continue
					}
					sr += int(f.RGBA[i])
					sg += int(f.RGBA[i+1])
					sb += int(f.RGBA[i+2])
					n++
				}
			}
			if n == 0 {
				out = append(out, 0, 0, 0)
				continue
			}
			out = append(out, byte(sr/n), byte(sg/n), byte(sb/n))
		}
	}
	return out
}

// Similarity returns 1 for identical signatures and 0 for maximally different.
func Similarity(a, b []byte) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var diff int
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		diff += d
	}
	return 1 - float64(diff)/float64(len(a)*255)
}

// ActionFromEvent converts a raw input event into a policy action. Unknown keys
// are ignored rather than guessed.
func ActionFromEvent(ev action.InputEvent) (domain.Action, bool) {
	switch ev.Type {
	case "key":
		key := normalizeKey(ev.Key)
		if key == "" {
			return domain.Action{}, false
		}
		return domain.Action{
			Kind:   "demo_key_" + key,
			Target: "game",
			Reason: "模仿你在相似画面按下的 " + key,
			Tags:   []string{"demo", "learned", "key"},
			Risk:   0.05,
			Input:  &domain.InputSpec{Type: "key", Key: key, DurationMS: 80},
		}, true
	case "mouse_down":
		button := strings.ToLower(strings.TrimSpace(ev.Button))
		if button == "" {
			button = "left"
		}
		return domain.Action{
			Kind:   "demo_click",
			Target: "demo_screen",
			Reason: "模仿你在相似画面点击的位置",
			Tags:   []string{"demo", "learned", "pointer"},
			Risk:   0.10,
			Input:  &domain.InputSpec{Type: "click", X: ev.X, Y: ev.Y, Button: button, ClickCount: 1},
		}, true
	}
	return domain.Action{}, false
}

// IntentFor is the generic, non-game-specific interpretation shown to the user.
func IntentFor(ev action.InputEvent) string {
	if ev.Type == "mouse_down" {
		return "点击界面"
	}
	switch normalizeKey(ev.Key) {
	case "enter", "space", "z":
		return "确认或攻击"
	case "esc":
		return "返回或取消"
	case "up", "down", "left", "right", "w", "a", "s", "d":
		return "移动或选择"
	case "shift", "x":
		return "特殊动作"
	}
	return "操作"
}

// NoteFor explains why the step is being kept, without claiming to know more
// than the observed input.
func NoteFor(ev action.InputEvent) string {
	if ev.Type == "mouse_down" {
		return fmt.Sprintf("你在画面相似的位置用 %s 键点击；AI 会把它当作可复用的点击示范。", ev.Button)
	}
	key := normalizeKey(ev.Key)
	return fmt.Sprintf("你在相似画面按了 %s（%s）；AI 会先复用这个动作，再根据结果调整。", key, IntentFor(ev))
}

func summarize(d *Demo) string {
	if d == nil || len(d.Steps) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, st := range d.Steps {
		counts[st.Intent]++
	}
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, n := range counts {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	top := "操作"
	if len(list) > 0 {
		top = list[0].k
	}
	return fmt.Sprintf("你示范了 %d 步，主要是%s。AI 已把这些画面和动作存成可复用示范，遇到相似画面会优先模仿。", len(d.Steps), top)
}

func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "return":
		return "enter"
	case "escape":
		return "esc"
	case "control":
		return "ctrl"
	}
	switch k {
	case "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m",
		"n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"enter", "esc", "space", "tab", "backspace", "shift", "ctrl", "alt",
		"up", "down", "left", "right", "f1", "f2", "f3", "f4":
		return k
	}
	return ""
}

func copyFrame(f domain.Frame) domain.Frame {
	f.RGBA = append([]byte(nil), f.RGBA...)
	return f
}

func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func thumbnailJPEG(f domain.Frame) ([]byte, error) {
	if f.Width <= 0 || f.Height <= 0 || len(f.RGBA) < f.Width*f.Height*4 {
		return nil, fmt.Errorf("teach: bad frame")
	}
	w, h := f.Width, f.Height
	scale := 1.0
	if w > thumbEdge || h > thumbEdge {
		scale = float64(thumbEdge) / float64(w)
		if sh := float64(thumbEdge) / float64(h); sh < scale {
			scale = sh
		}
	}
	dw := int(float64(w) * scale)
	dh := int(float64(h) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := y * h / dh
		for x := 0; x < dw; x++ {
			sx := x * w / dw
			si := (sy*w + sx) * 4
			di := y*img.Stride + x*4
			copy(img.Pix[di:di+4], f.RGBA[si:si+4])
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
