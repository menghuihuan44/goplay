// Package bridge connects an external mobile client (Android MediaProjection +
// AccessibilityService) to the core as a perception source and action executor.
//
// The core never injects into the target game process. The Android app captures
// its own screen with the user-granted MediaProjection API and executes gestures
// through the AccessibilityService; this package only moves frames and action
// requests across a WebSocket.
package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"time"

	"goplay/internal/action"
	"goplay/internal/domain"
)

// FrameMagic is the four-byte prefix of every binary frame.
const FrameMagic = "GPF1"

const (
	maxHeaderBytes = 64 << 10
	maxJPEGBytes   = 8 << 20
)

// FrameHeader is the JSON header carried inside a GPF1 frame.
type FrameHeader struct {
	CapturedAtMS int64  `json:"capturedAtMs"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Quality      int    `json:"quality"`
	Rotation     int    `json:"rotation"`
	Sequence     int64  `json:"sequence"`
	Source       string `json:"source"`
	InstanceID   string `json:"instanceId"`
	Bytes        int    `json:"bytes"`
}

// Transport is the write side of one client connection. SendJSON must be safe
// for concurrent use; the WebSocket handler supplies a serialized transport.
type Transport interface {
	SendJSON(v any) error
}

// AckResult is one client acknowledgement.
type AckResult struct {
	ID         string `json:"id"`
	OK         bool   `json:"ok"`
	ActionType string `json:"actionType"`
	ErrorCode  string `json:"errorCode"`
	Error      string `json:"error"`
	ExecutedMS int64  `json:"executedMs"`
}

// Stats is a snapshot of the bridge counters.
type Stats struct {
	Frames        int64     `json:"frames"`
	FramesDropped int64     `json:"frames_dropped"`
	Bytes         int64     `json:"bytes"`
	Actions       int64     `json:"actions"`
	ActionErrors  int64     `json:"action_errors"`
	Acks          int64     `json:"acks"`
	LastFrameAt   time.Time `json:"last_frame_at,omitempty"`
	LastSeq       int64     `json:"last_sequence"`
	Client        string    `json:"client,omitempty"`
	Device        string    `json:"device,omitempty"`
}

type frameResult struct {
	frame domain.Frame
	err   error
}

// Session is one Android instance's bridge. It implements perception.Source,
// action.Executor and action.HumanDetector.
type Session struct {
	id string

	mu      sync.Mutex
	tr      Transport
	latest  *domain.Frame
	lastSeq int64
	frames  int64
	drops   int64
	bytes   int64
	actions int64
	aerrs   int64
	acks    int64
	human   bool
	humanAt time.Time
	client  string
	device  string
	closed  bool

	waiters map[string]chan AckResult
	nextID  int64
	frameCh chan struct{}
	done    chan struct{}
}

// NewSession creates a bridge session for an instance id.
func NewSession(id string) *Session {
	return &Session{
		id: id, waiters: map[string]chan AckResult{},
		frameCh: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

// ID returns the instance id.
func (s *Session) ID() string { return s.id }

// Attach binds a connection transport and returns a detach function. Attaching a
// new transport replaces the old one; pending actions are released with an
// error because their acks can no longer arrive.
func (s *Session) Attach(tr Transport) func() {
	s.mu.Lock()
	s.tr = tr
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.tr == tr {
			s.tr = nil
		}
		s.mu.Unlock()
	}
}

// SetClient records hello metadata for status display.
func (s *Session) SetClient(client, device string) {
	s.mu.Lock()
	s.client, s.device = client, device
	s.mu.Unlock()
}

// SetHuman marks recent human interaction. The engine hands control over while
// this is active and resumes after the window expires.
func (s *Session) SetHuman(active bool, reason string) {
	s.mu.Lock()
	s.human = active
	if active {
		s.humanAt = time.Now()
	}
	s.mu.Unlock()
}

// ParseFrame validates and decodes one GPF1 binary message.
func ParseFrame(data []byte) (FrameHeader, []byte, error) {
	if len(data) < 9 {
		return FrameHeader{}, nil, errors.New("bridge: frame shorter than GPF1 header")
	}
	if string(data[:4]) != FrameMagic {
		return FrameHeader{}, nil, fmt.Errorf("bridge: bad frame magic %q", data[:4])
	}
	n := int(binary.BigEndian.Uint32(data[4:8]))
	if n <= 0 || n > maxHeaderBytes || len(data) < 8+n+1 {
		return FrameHeader{}, nil, errors.New("bridge: invalid frame header length")
	}
	var h FrameHeader
	if err := json.Unmarshal(data[8:8+n], &h); err != nil {
		return FrameHeader{}, nil, fmt.Errorf("bridge: decode frame header: %w", err)
	}
	img := data[8+n:]
	if len(img) == 0 || len(img) > maxJPEGBytes {
		return FrameHeader{}, nil, errors.New("bridge: invalid JPEG size")
	}
	if h.Bytes > 0 && h.Bytes != len(img) {
		return FrameHeader{}, nil, fmt.Errorf("bridge: header bytes=%d but JPEG is %d", h.Bytes, len(img))
	}
	return h, img, nil
}

// PushFrame decodes a JPEG frame and publishes it to Grab. Only the latest frame
// matters; a slow consumer causes older frames to be dropped.
func (s *Session) PushFrame(h FrameHeader, jpg []byte) error {
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return fmt.Errorf("bridge: decode JPEG: %w", err)
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	at := time.Now()
	if h.CapturedAtMS > 0 {
		at = time.UnixMilli(h.CapturedAtMS)
	}
	f := domain.Frame{
		Width: b.Dx(), Height: b.Dy(), RGBA: rgba.Pix, At: at,
		Source: "android-mediaprojection",
		Meta:   map[string]string{"rotation": fmt.Sprint(h.Rotation), "quality": fmt.Sprint(h.Quality), "sequence": fmt.Sprint(h.Sequence)},
	}
	s.mu.Lock()
	s.latest = &f
	s.lastSeq = h.Sequence
	s.frames++
	s.bytes += int64(len(jpg))
	s.mu.Unlock()
	select {
	case s.frameCh <- struct{}{}:
	default:
		s.mu.Lock()
		s.drops++
		s.mu.Unlock()
	}
	return nil
}

// Grab implements perception.Source. It waits for a fresh frame, but reuses a
// frame younger than three seconds so a 10 fps client can feed a faster loop.
func (s *Session) Grab(ctx context.Context) (domain.Frame, error) {
	s.mu.Lock()
	if s.latest != nil && time.Since(s.latest.At) < 1500*time.Millisecond {
		f := *s.latest
		s.mu.Unlock()
		return f, nil
	}
	s.mu.Unlock()
	select {
	case <-s.frameCh:
		s.mu.Lock()
		if s.latest != nil {
			f := *s.latest
			s.mu.Unlock()
			return f, nil
		}
		s.mu.Unlock()
		return domain.Frame{}, errors.New("bridge: frame signal without a frame")
	case <-ctx.Done():
		return domain.Frame{}, ctx.Err()
	}
}

// Describe implements perception.Source.
func (s *Session) Describe() string { return "android-mediaprojection-jpeg" }

// Close implements perception.Source and releases pending actions.
func (s *Session) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		for id, ch := range s.waiters {
			close(ch)
			delete(s.waiters, id)
		}
	}
	s.mu.Unlock()
	return nil
}

// Ack resolves a pending action.
func (s *Session) Ack(id string, ok bool, errCode, errText, actionType string, executedMS int64) {
	s.mu.Lock()
	ch := s.waiters[id]
	if ch != nil {
		delete(s.waiters, id)
	}
	s.acks++
	s.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- AckResult{ID: id, OK: ok, ErrorCode: errCode, Error: errText, ActionType: actionType, ExecutedMS: executedMS}
}

// Do implements action.Executor.
func (s *Session) Do(ctx context.Context, a domain.Action) error {
	act, err := androidAction(a)
	if err != nil {
		s.mu.Lock()
		s.aerrs++
		s.mu.Unlock()
		return err
	}
	id := fmt.Sprintf("act-%d", atomic.AddInt64(&s.nextID, 1))
	act["id"] = id
	ch := make(chan AckResult, 1)
	s.mu.Lock()
	tr := s.tr
	if tr == nil {
		s.mu.Unlock()
		return errors.New("bridge: no Android client connected")
	}
	s.waiters[id] = ch
	s.actions++
	s.mu.Unlock()

	if err := tr.SendJSON(map[string]any{"type": "action", "id": id, "action": act}); err != nil {
		s.mu.Lock()
		delete(s.waiters, id)
		s.aerrs++
		s.mu.Unlock()
		return fmt.Errorf("bridge: send action: %w", err)
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return errors.New("bridge: connection closed before action ack")
		}
		if !res.OK {
			msg := res.Error
			if msg == "" {
				msg = res.ErrorCode
			}
			return fmt.Errorf("android action %s failed: %s", res.ActionType, msg)
		}
		return nil
	case <-time.After(15 * time.Second):
		s.mu.Lock()
		delete(s.waiters, id)
		s.aerrs++
		s.mu.Unlock()
		return fmt.Errorf("bridge: action %s ack timeout", id)
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.waiters, id)
		s.mu.Unlock()
		return ctx.Err()
	}
}

// State implements action.HumanDetector. A client human signal is considered
// active for 2.5 seconds, so the loop resumes automatically if the client stops
// reporting and never traps the AI in human-control mode.
func (s *Session) State(context.Context) action.HumanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.human && time.Since(s.humanAt) > 2500*time.Millisecond {
		s.human = false
	}
	if !s.human {
		return action.HumanState{}
	}
	return action.HumanState{Active: true, Since: s.humanAt, Reason: "android-client"}
}

// Stats returns bridge counters.
func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{
		Frames: s.frames, FramesDropped: s.drops, Bytes: s.bytes,
		Actions: s.actions, ActionErrors: s.aerrs, Acks: s.acks,
		LastSeq: s.lastSeq, Client: s.client, Device: s.device,
	}
	if s.latest != nil {
		st.LastFrameAt = s.latest.At
	}
	return st
}

func androidAction(a domain.Action) (map[string]any, error) {
	in := a.Input
	if in == nil {
		return nil, fmt.Errorf("bridge: action %q has no input mapping", a.Kind)
	}
	typ := in.Type
	if typ == "" {
		typ = a.Kind
	}
	m := map[string]any{"type": typ, "note": a.Reason}
	switch typ {
	case "tap", "click":
		m["type"] = "tap"
		m["x"], m["y"] = in.X, in.Y
	case "swipe":
		m["x"], m["y"], m["x2"], m["y2"] = in.X, in.Y, in.X2, in.Y2
		if in.DurationMS > 0 {
			m["durationMs"] = in.DurationMS
		}
	case "long_press", "longpress":
		m["type"] = "long_press"
		m["x"], m["y"] = in.X, in.Y
		if in.DurationMS > 0 {
			m["durationMs"] = in.DurationMS
		}
	case "back":
	case "wait", "sleep":
		m["type"] = "wait"
		if in.DurationMS > 0 {
			m["durationMs"] = in.DurationMS
		}
	case "text":
		m["text"] = in.Text
	case "key":
		return nil, fmt.Errorf("bridge: keyboard action %q is not supported by the touch client", in.Key)
	case "touch", "gamepad", "move":
		return nil, fmt.Errorf("bridge: action type %q has no Android mapping", typ)
	default:
		return nil, fmt.Errorf("bridge: unsupported Android action type %q", typ)
	}
	return m, nil
}
