package service

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func TestServiceStartsWithEphemeralPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc, err := Start(ctx, Options{DataDir: t.TempDir(), Addr: "127.0.0.1:0", LLMMode: "offline"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- svc.Serve(ctx) }()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-served:
		t.Fatalf("Serve returned early: %v", err)
	default:
	}

	client := &http.Client{Timeout: 3 * time.Second}
	var health struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
		Version string `json:"version"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(svc.URL() + "api/health")
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&health)
			resp.Body.Close()
			if err == nil && health.OK {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
			t.Fatalf("health endpoint not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health.Service != "goplay" {
		t.Fatalf("service = %q, want goplay", health.Service)
	}
	if health.Version == "" {
		t.Fatal("health version is empty")
	}
	if !strings.HasPrefix(svc.Addr(), "127.0.0.1:") {
		t.Fatalf("addr = %q", svc.Addr())
	}

	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := svc.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
