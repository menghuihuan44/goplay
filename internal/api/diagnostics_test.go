package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"goplay/internal/domain"
)

func TestClassifyDiagnostic(t *testing.T) {
	tests := []struct {
		name       string
		in         diagInput
		code       string
		fixable    bool
		phase      string
		suggestion string
	}{
		{
			name: "healthy offline mock",
			in:   diagInput{Status: domain.StatusRunning, Source: "mock", LLMMode: "offline"},
			code: "ok",
		},
		{
			name:       "external control",
			in:         diagInput{Status: domain.StatusHuman, Source: "android", Human: true},
			code:       "external_control",
			fixable:    true,
			phase:      "human_control",
			suggestion: "release_human",
		},
		{
			name:       "no android client",
			in:         diagInput{Status: domain.StatusRunning, Source: "android"},
			code:       "android_no_client",
			fixable:    true,
			phase:      "capture",
			suggestion: "open_permissions",
		},
		{
			name:       "frame timeout",
			in:         diagInput{Status: domain.StatusRunning, Source: "android", BridgeConnected: true, Frames: 50, LastFrameAgeMS: 9000},
			code:       "android_no_client",
			fixable:    true,
			phase:      "capture",
			suggestion: "start_capture",
		},
		{
			name:       "ack timeout",
			in:         diagInput{Status: domain.StatusRunning, Source: "android", BridgeConnected: true, Frames: 10, LastFrameAgeMS: 100, LastError: "bridge: action tap ack timeout"},
			code:       "android_ack_timeout",
			fixable:    true,
			phase:      "capture",
			suggestion: "retry_start",
		},
		{
			name:       "action failed",
			in:         diagInput{Status: domain.StatusRunning, Source: "android", BridgeConnected: true, Frames: 10, LastFrameAgeMS: 100, LastError: "android action tap failed: node missing"},
			code:       "android_action_failed",
			fixable:    true,
			phase:      "capture",
			suggestion: "open_permissions",
		},
		{
			name:       "capture failed",
			in:         diagInput{Status: domain.StatusRunning, Source: "mock", LastError: "capture: no display"},
			code:       "capture_failed",
			fixable:    true,
			phase:      "capture",
			suggestion: "start_capture",
		},
		{
			name:       "llm unavailable",
			in:         diagInput{Status: domain.StatusRunning, Source: "mock", LLMMode: "api"},
			code:       "llm_unavailable",
			fixable:    true,
			phase:      "model",
			suggestion: "open_ai",
		},
		{
			name:       "model missing",
			in:         diagInput{Status: domain.StatusRunning, Source: "mock", LLMMode: "local", ModelMissing: true},
			code:       "model_missing",
			fixable:    true,
			phase:      "model",
			suggestion: "open_ai",
		},
		{
			name:       "unknown error",
			in:         diagInput{Status: domain.StatusRunning, Source: "mock", LastError: "boom"},
			code:       "unknown",
			phase:      "running",
			suggestion: "open_logs",
		},
		{
			name:       "error without detail",
			in:         diagInput{Status: domain.StatusError, Source: "mock"},
			code:       "unknown",
			phase:      "error",
			suggestion: "open_logs",
		},
	}

	allowed := map[string]bool{
		"open_permissions": true, "start_capture": true, "retry_start": true,
		"open_ai": true, "release_human": true, "open_logs": true,
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := classifyDiagnostic(tt.in)
			if res.Code != tt.code {
				t.Fatalf("code = %q, want %q (title=%q cause=%q)", res.Code, tt.code, res.Title, res.Cause)
			}
			if res.OK != (tt.code == "ok") {
				t.Fatalf("ok = %v for code %q", res.OK, res.Code)
			}
			if res.UserFixable != tt.fixable {
				t.Fatalf("user_fixable = %v, want %v", res.UserFixable, tt.fixable)
			}
			if tt.phase != "" && res.Phase != tt.phase {
				t.Fatalf("phase = %q, want %q", res.Phase, tt.phase)
			}
			if tt.suggestion != "" {
				found := false
				for _, s := range res.Suggestions {
					if !allowed[s.Action] {
						t.Fatalf("invalid suggestion action %q", s.Action)
					}
					if s.Action == tt.suggestion {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing suggestion action %q in %+v", tt.suggestion, res.Suggestions)
				}
			}
			for _, c := range res.Checks {
				if c.ID == "" || c.Label == "" || c.Detail == "" {
					t.Fatalf("empty check: %+v", c)
				}
			}
		})
	}
}

func TestDiagnosticsHandlerReportsMockInstance(t *testing.T) {
	ts, mgr := newTestServer(t)
	createDemo(t, ts)
	waitFor(t, "instance running", func() bool {
		v, err := mgr.Get("demo")
		return err == nil && v.Status == domain.StatusRunning
	})

	srv := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/instances/demo/diagnostics", nil)
	req.SetPathValue("id", "demo")
	rec := httptest.NewRecorder()
	srv.instanceDiagnostics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got diagResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if got.Code != "ok" || !got.OK {
		t.Fatalf("diagnostics code = %q ok=%v title=%q detail=%q", got.Code, got.OK, got.Title, got.Detail)
	}
	if got.Instance.ID != "demo" || got.Instance.Source != "mock" || got.Instance.Status != string(domain.StatusRunning) {
		t.Fatalf("diagnostics instance = %+v", got.Instance)
	}
	if len(got.Checks) == 0 {
		t.Fatal("diagnostics has no checks")
	}
	if got.Bridge.LastFrameAgeMS != -1 {
		t.Fatalf("mock bridge last_frame_age_ms = %d, want -1", got.Bridge.LastFrameAgeMS)
	}
}

func TestFrameHandlerServesJPEG(t *testing.T) {
	ts, mgr := newTestServer(t)
	createDemo(t, ts)
	waitFor(t, "frame available", func() bool {
		v, err := mgr.Get("demo")
		return err == nil && len(v.FrameJPEG) > 0
	})
	view, err := mgr.Get("demo")
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/instances/demo/frame", nil)
	req.SetPathValue("id", "demo")
	rec := httptest.NewRecorder()
	srv.instanceFrame(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("frame status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("frame content type = %q", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("frame body is not JPEG: %x", rec.Body.Bytes()[:3])
	}
	if !bytes.Equal(rec.Body.Bytes(), view.FrameJPEG) {
		t.Fatal("frame handler did not return the snapshot frame")
	}
}
