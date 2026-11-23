package localmodel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrRuntimeNotFound means no llama-server executable was configured or
	// found on PATH. No binary is bundled with goplay.
	ErrRuntimeNotFound = errors.New("localmodel: llama-server runtime not found")
	// ErrModelNotFound means the GGUF path does not exist.
	ErrModelNotFound = errors.New("localmodel: model file not found")
)

// ServerConfig describes one llama.cpp llama-server child process. The server is
// always bound to a loopback host; the binary is external and never embedded.
type ServerConfig struct {
	RuntimePath    string        `json:"runtime_path,omitempty"`
	ModelPath      string        `json:"model_path"`
	Host           string        `json:"host"`
	Port           int           `json:"port"`
	ContextTokens  int           `json:"context_tokens"`
	Threads        int           `json:"threads"`
	ExtraArgs      []string      `json:"extra_args,omitempty"`
	StartupTimeout time.Duration `json:"-"`
	StopTimeout    time.Duration `json:"-"`
	HealthInterval time.Duration `json:"-"`
}

func (c ServerConfig) withDefaults() ServerConfig {
	c.Host = strings.TrimSpace(c.Host)
	if strings.HasPrefix(c.Host, "[") && strings.HasSuffix(c.Host, "]") {
		c.Host = strings.TrimSuffix(strings.TrimPrefix(c.Host, "["), "]")
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = 8421
	}
	if c.ContextTokens == 0 {
		c.ContextTokens = 4096
	}
	if c.Threads == 0 {
		c.Threads = 4
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = 120 * time.Second
	}
	if c.StopTimeout <= 0 {
		c.StopTimeout = 10 * time.Second
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 200 * time.Millisecond
	}
	if c.ExtraArgs != nil {
		c.ExtraArgs = append([]string(nil), c.ExtraArgs...)
	}
	return c
}

func (c ServerConfig) validate() error {
	if strings.TrimSpace(c.ModelPath) == "" {
		return errors.New("localmodel: ModelPath is required")
	}
	if !isLoopbackHost(c.Host) {
		return fmt.Errorf("localmodel: host %q is not loopback; the local server must bind to 127.0.0.1/localhost/::1", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("localmodel: port %d out of range 1..65535", c.Port)
	}
	if c.ContextTokens < 512 {
		return fmt.Errorf("localmodel: ContextTokens %d is too small (minimum 512)", c.ContextTokens)
	}
	if c.Threads < 1 {
		return fmt.Errorf("localmodel: Threads must be >= 1")
	}
	for _, a := range c.ExtraArgs {
		low := strings.ToLower(strings.TrimSpace(a))
		if low == "--host" || strings.HasPrefix(low, "--host=") {
			return errors.New("localmodel: ExtraArgs may not override --host; the server must stay on loopback")
		}
	}
	return nil
}

func isLoopbackHost(h string) bool {
	h = strings.TrimSpace(h)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// BuildServerArgs returns the exact llama-server argument vector. It is exported
// so callers can log or test startup without launching a process.
func BuildServerArgs(cfg ServerConfig) ([]string, error) {
	c := cfg.withDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	args := []string{
		"--model", c.ModelPath,
		"--port", strconv.Itoa(c.Port),
		"--ctx-size", strconv.Itoa(c.ContextTokens),
		"--threads", strconv.Itoa(c.Threads),
		"--host", c.Host,
	}
	args = append(args, c.ExtraArgs...)
	return args, nil
}

// ResolveRuntime resolves RuntimePath, then GOPLAY_LLAMA_SERVER, then PATH.
// It returns a clear, actionable error if llama-server is unavailable.
func ResolveRuntime(explicit string) (string, error) {
	if strings.TrimSpace(explicit) == "" {
		explicit = strings.TrimSpace(os.Getenv("GOPLAY_LLAMA_SERVER"))
	}
	if explicit != "" {
		fi, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("%w: %q: %v", ErrRuntimeNotFound, explicit, err)
		}
		if fi.IsDir() {
			return "", fmt.Errorf("%w: %q is a directory", ErrRuntimeNotFound, explicit)
		}
		return explicit, nil
	}
	p, err := exec.LookPath("llama-server")
	if err != nil {
		return "", fmt.Errorf("%w: install llama.cpp and put llama-server on PATH, or set local.runtime_path / GOPLAY_LLAMA_SERVER. goplay does not bundle the binary", ErrRuntimeNotFound)
	}
	return p, nil
}

// Server is a managed llama-server child process.
type Server struct {
	cfg         ServerConfig
	cmd         *exec.Cmd
	args        []string
	runtimePath string
	tail        *tailBuffer
	health      *http.Client
	exited      chan struct{}

	mu      sync.Mutex
	exitErr error
	stopped bool
}

// StartServer starts llama-server, waits for /health, and returns a handle. The
// startup context only bounds process start + health polling; it does not kill
// the server after startup.
func StartServer(ctx context.Context, cfg ServerConfig) (*Server, error) {
	c := cfg.withDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	rt, err := ResolveRuntime(c.RuntimePath)
	if err != nil {
		return nil, err
	}
	absModel, err := filepath.Abs(c.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("resolve model path: %w", err)
	}
	fi, err := os.Stat(absModel)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrModelNotFound, absModel, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrModelNotFound, absModel)
	}
	c.ModelPath = absModel
	args, err := BuildServerArgs(c)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(rt, args...)
	tail := newTailBuffer(64 << 10)
	cmd.Stdout = tail
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start llama-server (%s): %w", rt, err)
	}
	s := &Server{
		cfg:         c,
		cmd:         cmd,
		args:        args,
		runtimePath: rt,
		tail:        tail,
		health:      &http.Client{Timeout: 2 * time.Second},
		exited:      make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.exitErr = err
		s.mu.Unlock()
		close(s.exited)
	}()

	startupCtx, cancel := context.WithTimeout(ctx, c.StartupTimeout)
	defer cancel()
	if err := s.waitHealthy(startupCtx); err != nil {
		s.forceStop()
		return nil, fmt.Errorf("llama-server failed to become healthy at %s: %w; log tail: %s",
			s.HealthURL(), err, tail.String())
	}
	return s, nil
}

func (s *Server) waitHealthy(ctx context.Context) error {
	var last error
	for {
		if err := s.checkHealth(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("%w (last health error: %v)", ctx.Err(), last)
			}
			return ctx.Err()
		case <-s.exited:
			return fmt.Errorf("process exited before health check passed: %v", s.exitError())
		case <-time.After(s.cfg.HealthInterval):
		}
	}
}

func (s *Server) checkHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.HealthURL(), nil)
	if err != nil {
		return err
	}
	resp, err := s.health.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// BaseURL is the OpenAI-compatible base URL (http://host:port/v1).
func (s *Server) BaseURL() string {
	return "http://" + net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)) + "/v1"
}

// HealthURL is the llama-server readiness endpoint.
func (s *Server) HealthURL() string {
	return "http://" + net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)) + "/health"
}

// ChatCompletionsURL is the endpoint used by Bench.
func (s *Server) ChatCompletionsURL() string { return s.BaseURL() + "/chat/completions" }

// PID returns the child process id (0 if not started).
func (s *Server) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Args returns a copy of the argument vector.
func (s *Server) Args() []string { return append([]string(nil), s.args...) }

// RuntimePath is the resolved llama-server executable.
func (s *Server) RuntimePath() string { return s.runtimePath }

// ModelPath is the absolute GGUF path.
func (s *Server) ModelPath() string { return s.cfg.ModelPath }

// Exited is closed when the child process exits.
func (s *Server) Exited() <-chan struct{} { return s.exited }

// Wait blocks until the process exits or ctx is done.
func (s *Server) Wait(ctx context.Context) error {
	select {
	case <-s.exited:
		return s.exitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop asks the child to terminate. On Unix it sends SIGTERM and escalates to
// Kill after StopTimeout; Windows has no portable SIGTERM for a child without a
// console group, so it uses Kill and then waits. Stop is idempotent.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = requestStop(cmd.Process)
	timer := time.NewTimer(s.cfg.StopTimeout)
	defer timer.Stop()
	select {
	case <-s.exited:
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return s.waitKilled(2*time.Second, ctx.Err())
	case <-timer.C:
		_ = cmd.Process.Kill()
		return s.waitKilled(2*time.Second, fmt.Errorf("llama-server did not exit within %s", s.cfg.StopTimeout))
	}
}

func (s *Server) waitKilled(timeout time.Duration, cause error) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.exited:
		return cause
	case <-timer.C:
		return fmt.Errorf("%w; process did not exit after Kill", cause)
	}
}

func (s *Server) forceStop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	select {
	case <-s.exited:
	case <-time.After(3 * time.Second):
	}
}

func (s *Server) exitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

// tailBuffer keeps the last max bytes written by the child process.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
