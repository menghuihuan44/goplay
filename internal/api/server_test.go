package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"goplay/internal/app"
	"goplay/internal/config"
	"goplay/internal/llm"
)

func newTestServer(t *testing.T) (*httptest.Server, *app.Manager) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.LLM.Mode = "offline"
	cfg.MaxInstances = 4
	cfg.TickMS = 20
	cfg.HumanResumeMS = 80
	cfg.MemoryBudgetKB = 256
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := llm.NewRouter("offline", nil, nil)
	mgr, err := app.New(cfg, nil, router, log)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Close(t.Context())
	})
	ts := httptest.NewServer(New(mgr, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts, mgr
}

func doJSON(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %v: %s", method, url, err, b)
		}
	}
	return res.StatusCode
}

func createDemo(t *testing.T, ts *httptest.Server) {
	t.Helper()
	code := doJSON(t, http.MethodPost, ts.URL+"/api/instances", map[string]any{
		"id": "demo", "name": "Demo", "profile_id": "mock_dungeon",
		"seed": 7, "max_episodes": 1, "tick_ms": 20,
		"human_resume_ms": 80, "auto_start": true,
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create status = %d", code)
	}
}

type snapView struct {
	Status  string `json:"status"`
	Actions int64  `json:"actions"`
	Stats   struct {
		Actions int64 `json:"actions"`
	} `json:"stats"`
	State struct {
		Values map[string]float64 `json:"values"`
	} `json:"state"`
	Control struct {
		Hard    []map[string]any   `json:"hard"`
		Weights map[string]float64 `json:"weights"`
	} `json:"control"`
	Chat []struct {
		Role string `json:"role"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"chat"`
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDashboardStaticAndInstanceLifecycle(t *testing.T) {
	ts, _ := newTestServer(t)
	createDemo(t, ts)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "灵动岛") {
		t.Fatalf("dashboard missing: status=%d", res.StatusCode)
	}
	res, err = http.Get(ts.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "/api/instances") {
		t.Fatalf("app.js not served correctly")
	}

	var snap snapView
	waitFor(t, "running status and actions", func() bool {
		doJSON(t, http.MethodGet, ts.URL+"/api/instances/demo", nil, &snap)
		return snap.Status == "running" && snap.Stats.Actions > 2
	})
}

func TestMessageBecomesInstanceConstraint(t *testing.T) {
	ts, _ := newTestServer(t)
	createDemo(t, ts)
	waitFor(t, "first actions", func() bool {
		var s snapView
		doJSON(t, http.MethodGet, ts.URL+"/api/instances/demo", nil, &s)
		return s.Stats.Actions > 1
	})
	code := doJSON(t, http.MethodPost, ts.URL+"/api/instances/demo/message", map[string]any{"text": "先别打那个 Boss"}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("message status = %d", code)
	}
	var snap snapView
	waitFor(t, "hard constraint at boundary", func() bool {
		doJSON(t, http.MethodGet, ts.URL+"/api/instances/demo", nil, &snap)
		return len(snap.Control.Hard) > 0
	})
	if got := snap.Control.Hard[0]["text"]; got != "先别打那个 Boss" {
		t.Fatalf("constraint text = %v", got)
	}
	deny := snap.Control.Hard[0]["deny_actions"]
	if deny == nil {
		t.Fatalf("constraint has no deny_actions: %+v", snap.Control.Hard[0])
	}
	seen := false
	for _, chat := range snap.Chat {
		if chat.Role == "assistant" && chat.Kind == "constraint" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("assistant boundary reply missing: %+v", snap.Chat)
	}
}

func TestGlobalPreferenceIsSoftWeight(t *testing.T) {
	ts, _ := newTestServer(t)
	createDemo(t, ts)
	code := doJSON(t, http.MethodPost, ts.URL+"/api/memory/global/preferences", map[string]any{
		"text": "更谨慎", "weights": map[string]float64{"risk_aversion": 1}, "source": "test",
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("preference status = %d", code)
	}
	var snap snapView
	waitFor(t, "soft weight in control", func() bool {
		doJSON(t, http.MethodGet, ts.URL+"/api/instances/demo", nil, &snap)
		return snap.Control.Weights["risk_aversion"] > 0
	})
	if len(snap.Control.Hard) != 0 {
		t.Fatalf("global preference must not create hard constraints: %+v", snap.Control.Hard)
	}
}

func TestLocalRecommendForMobileDevice(t *testing.T) {
	ts, _ := newTestServer(t)
	var out map[string]any
	code := doJSON(t, http.MethodPost, ts.URL+"/api/local/recommend", map[string]any{
		"device": "Pixel 8", "ramMb": 7912, "abis": []string{"arm64-v8a"},
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("recommend status = %d (%v)", code, out)
	}
	if out["tier"] == "" || out["tier"] == nil {
		t.Fatalf("missing tier: %v", out)
	}
	if out["downloadRequired"] != true {
		t.Fatalf("mobile recommendation must not claim the model is bundled: %v", out)
	}
	if n, _ := out["approxBytes"].(float64); n <= 0 {
		t.Fatalf("approxBytes = %v", out["approxBytes"])
	}
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/local/recommend", map[string]any{"ramMb": 0}, nil); code != http.StatusBadRequest {
		t.Fatalf("zero ram status = %d", code)
	}
}

func TestWebSocketHelloAndSnapshot(t *testing.T) {
	ts, _ := newTestServer(t)
	createDemo(t, ts)
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/instance/demo"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	sawHello, sawSnapshot := false, false
	for i := 0; i < 300; i++ {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		switch msg["type"] {
		case "hello":
			sawHello = true
		case "snapshot":
			sawSnapshot = true
		}
		if sawHello && sawSnapshot {
			return
		}
	}
	t.Fatalf("hello=%v snapshot=%v", sawHello, sawSnapshot)
}
