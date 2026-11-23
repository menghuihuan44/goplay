package localmodel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildServerArgs(t *testing.T) {
	args, err := BuildServerArgs(ServerConfig{
		ModelPath:     "model.gguf",
		Host:          "127.0.0.1",
		Port:          9000,
		ContextTokens: 2048,
		Threads:       6,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "model.gguf", "--port", "9000", "--ctx-size", "2048", "--threads", "6", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}

	args, err = BuildServerArgs(ServerConfig{ModelPath: "m.gguf"})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"--model", "m.gguf", "--port", "8421", "--ctx-size", "4096", "--threads", "4", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("default args = %#v, want %#v", args, want)
	}

	args, err = BuildServerArgs(ServerConfig{
		ModelPath: "m.gguf",
		Host:      "[::1]",
		Port:      8421,
		ExtraArgs: []string{"--no-warmup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := args[len(args)-3]; got != "--host" {
		t.Errorf("host flag = %q", got)
	}
	if got := args[len(args)-2]; got != "::1" {
		t.Errorf("normalized IPv6 host = %q", got)
	}
	if got := args[len(args)-1]; got != "--no-warmup" {
		t.Errorf("extra arg = %q", got)
	}
}

func TestBuildServerArgsValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{"missing model", ServerConfig{}, "ModelPath"},
		{"non-loopback", ServerConfig{ModelPath: "m", Host: "0.0.0.0"}, "loopback"},
		{"bad port", ServerConfig{ModelPath: "m", Port: 70000}, "port"},
		{"tiny context", ServerConfig{ModelPath: "m", ContextTokens: 100}, "ContextTokens"},
		{"bad threads", ServerConfig{ModelPath: "m", Threads: -1}, "Threads"},
		{"host override", ServerConfig{ModelPath: "m", ExtraArgs: []string{"--host", "0.0.0.0"}}, "may not override"},
		{"host equals override", ServerConfig{ModelPath: "m", ExtraArgs: []string{"--host=0.0.0.0"}}, "may not override"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildServerArgs(c.cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want substring %q", err, c.want)
			}
		})
	}
}

func TestResolveRuntime(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(runtimePath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRuntime(runtimePath)
	if err != nil || got != runtimePath {
		t.Fatalf("ResolveRuntime(explicit) = %q, %v", got, err)
	}
	t.Setenv("GOPLAY_LLAMA_SERVER", runtimePath)
	got, err = ResolveRuntime("")
	if err != nil || got != runtimePath {
		t.Fatalf("ResolveRuntime(env) = %q, %v", got, err)
	}

	if _, err := ResolveRuntime(filepath.Join(dir, "missing")); !errors.Is(err, ErrRuntimeNotFound) {
		t.Errorf("missing runtime error = %v", err)
	}
	if _, err := ResolveRuntime(dir); !errors.Is(err, ErrRuntimeNotFound) {
		t.Errorf("directory runtime error = %v", err)
	}
}

func TestStartServerReportsMissingInputs(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StartServer(context.Background(), ServerConfig{
		ModelPath:   modelPath,
		RuntimePath: filepath.Join(dir, "no-such-llama-server"),
	})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("missing runtime error = %v", err)
	}

	runtimePath := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(runtimePath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = StartServer(context.Background(), ServerConfig{
		ModelPath:   filepath.Join(dir, "missing.gguf"),
		RuntimePath: runtimePath,
	})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("missing model error = %v", err)
	}

	_, err = StartServer(context.Background(), ServerConfig{
		ModelPath:   modelPath,
		RuntimePath: runtimePath,
		Host:        "0.0.0.0",
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}
}

func TestServerCheckHealthAndStopIdempotent(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("health path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ok.Close()
	host, portStr, err := net.SplitHostPort(ok.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	s := &Server{cfg: ServerConfig{Host: host, Port: port}, health: &http.Client{Timeout: time.Second}}
	if got := s.HealthURL(); got != "http://"+net.JoinHostPort(host, portStr)+"/health" {
		t.Errorf("HealthURL = %q", got)
	}
	if got := s.ChatCompletionsURL(); !strings.HasSuffix(got, "/v1/chat/completions") {
		t.Errorf("ChatCompletionsURL = %q", got)
	}
	if err := s.checkHealth(context.Background()); err != nil {
		t.Errorf("healthy check: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	host, portStr, _ = net.SplitHostPort(bad.Listener.Addr().String())
	port, _ = strconv.Atoi(portStr)
	s = &Server{cfg: ServerConfig{Host: host, Port: port}, health: &http.Client{Timeout: time.Second}}
	if err := s.checkHealth(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("unhealthy check error = %v", err)
	}

	empty := &Server{}
	if err := empty.Stop(context.Background()); err != nil {
		t.Fatalf("Stop zero server: %v", err)
	}
	if err := empty.Stop(context.Background()); err != nil {
		t.Fatalf("Stop should be idempotent: %v", err)
	}
}

func TestTailBufferKeepsTail(t *testing.T) {
	tb := newTailBuffer(8)
	if _, err := tb.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Write([]byte("ghijkl")); err != nil {
		t.Fatal(err)
	}
	if got := tb.String(); got != "efghijkl" {
		t.Fatalf("tail = %q, want efghijkl", got)
	}
}
