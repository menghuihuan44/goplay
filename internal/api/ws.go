package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"goplay/internal/app"
	"goplay/internal/bridge"
)

type wsMessage struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	ID         string          `json:"id,omitempty"`
	Client     string          `json:"client,omitempty"`
	Device     string          `json:"device,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Active     *bool           `json:"active,omitempty"`
	OK         bool            `json:"ok,omitempty"`
	ActionType string          `json:"actionType,omitempty"`
	ErrorCode  string          `json:"errorCode,omitempty"`
	ErrorText  string          `json:"error,omitempty"`
	ExecutedMS int64           `json:"executedMs,omitempty"`
}

type wsIn struct {
	msg    wsMessage
	binary []byte
}

// wsSink serializes writes from the event loop and from executor goroutines.
type wsSink struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsSink) SendJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return w.conn.WriteJSON(v)
}

func (s *Server) instanceWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.mgr.Get(id); err != nil {
		// The Android client uses a fixed instance id on first launch. Provision
		// it automatically when the client identifies itself as the mobile app.
		if r.Header.Get("X-GoPlay-Client") == "android" {
			_, cerr := s.mgr.Create(androidSpec(id))
			if cerr != nil {
				if _, err2 := s.mgr.Get(id); err2 != nil {
					writeError(w, http.StatusNotFound, err.Error())
					return
				}
			}
		} else {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
	}
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(16 << 20)

	sink := &wsSink{conn: conn}
	send := func(v any) bool { return sink.SendJSON(v) == nil }

	var sess *bridge.Session
	if got, ok := s.mgr.AndroidSession(id); ok {
		sess = got
		detach := sess.Attach(sink)
		defer detach()
	}

	events, unsub := s.mgr.Bus().Subscribe(256)
	defer unsub()

	in := make(chan wsIn, 16)
	go readWS(conn, in)

	if view, err := s.mgr.Get(id); err == nil {
		if !send(map[string]any{"type": "hello", "protocol": 1, "instance": view}) {
			return
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case item, ok := <-in:
			if !ok {
				return
			}
			if item.binary != nil {
				if sess == nil {
					if !send(map[string]any{"type": "error", "error": "this instance does not accept binary frames"}) {
						return
					}
					continue
				}
				h, jpg, err := bridge.ParseFrame(item.binary)
				if err != nil {
					if !send(map[string]any{"type": "error", "error": err.Error()}) {
						return
					}
					continue
				}
				if h.InstanceID != "" && h.InstanceID != id {
					if !send(map[string]any{"type": "error", "error": "frame instanceId does not match socket path"}) {
						return
					}
					continue
				}
				if err := sess.PushFrame(h, jpg); err != nil {
					if !send(map[string]any{"type": "error", "error": err.Error()}) {
						return
					}
				}
				continue
			}
			msg := item.msg
			switch msg.Type {
			case "snapshot":
				if view, err := s.mgr.Get(id); err == nil {
					if !send(map[string]any{"type": "snapshot", "instance": view}) {
						return
					}
				}
			case "hello":
				if sess != nil {
					sess.SetClient(msg.Client, msg.Device)
				}
				if !send(map[string]any{"type": "hello_ack", "protocol": 1, "instance": id}) {
					return
				}
			case "ack":
				if sess != nil {
					sess.Ack(msg.ID, msg.OK, msg.ErrorCode, msg.ErrorText, msg.ActionType, msg.ExecutedMS)
				}
			case "human":
				if sess != nil && msg.Active != nil {
					sess.SetHuman(*msg.Active, msg.Reason)
					if !send(map[string]any{"type": "ack", "human": *msg.Active, "note": "human control updated"}) {
						return
					}
				}
			case "message":
				if err := s.mgr.Submit(id, msg.Text); err != nil {
					if !send(map[string]any{"type": "error", "error": err.Error()}) {
						return
					}
				} else if !send(map[string]any{"type": "ack", "text": msg.Text, "note": "queued for next event boundary"}) {
					return
				}
			case "pause":
				if !s.wsControl(send, s.mgr.Pause, id) {
					return
				}
			case "resume":
				if !s.wsControl(send, s.mgr.Resume, id) {
					return
				}
			case "start":
				if !s.wsControl(send, s.mgr.Start, id) {
					return
				}
			case "stop":
				if !s.wsControl(send, s.mgr.Stop, id) {
					return
				}
			case "ping":
				if !send(map[string]any{"type": "pong", "at": time.Now()}) {
					return
				}
			case "frame":
				if !send(map[string]any{"type": "error", "error": "send frames as GPF1 binary WebSocket messages"}) {
					return
				}
			default:
				if !send(map[string]any{"type": "error", "error": "unknown message type " + msg.Type}) {
					return
				}
			}
		case e := <-events:
			if e.InstanceID != "" && e.InstanceID != id {
				continue
			}
			if !send(map[string]any{"type": "event", "event": e}) {
				return
			}
		case <-ticker.C:
			if view, err := s.mgr.Get(id); err == nil {
				if !send(map[string]any{"type": "snapshot", "instance": view}) {
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) wsControl(send func(any) bool, fn func(string) error, id string) bool {
	if err := fn(id); err != nil {
		return send(map[string]any{"type": "error", "error": err.Error()})
	}
	if view, err := s.mgr.Get(id); err == nil {
		return send(map[string]any{"type": "snapshot", "instance": view})
	}
	return true
}

func (s *Server) eventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	sink := &wsSink{conn: conn}
	events, unsub := s.mgr.Bus().Subscribe(512)
	defer unsub()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	if err := sink.SendJSON(map[string]any{"type": "hello", "recent": s.mgr.Events("", 80)}); err != nil {
		return
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e := <-events:
			if err := sink.SendJSON(map[string]any{"type": "event", "event": e}); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-readDone:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func readWS(conn *websocket.Conn, out chan<- wsIn) {
	defer close(out)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		item := wsIn{}
		if mt == websocket.BinaryMessage {
			item.binary = data
		} else {
			if err := json.Unmarshal(data, &item.msg); err != nil {
				continue
			}
		}
		select {
		case out <- item:
		case <-time.After(3 * time.Second):
			return
		}
	}
}

func androidSpec(id string) app.Spec {
	return app.Spec{
		ID: id, Name: "Android " + id, ProfileID: "generic_touch",
		SourceKind: app.SourceAndroid, AutoStart: true,
	}
}
