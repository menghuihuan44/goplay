package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Mock is a deterministic provider used by tests and offline demos. It is not
// a model: it only proves the LLM plumbing (prompt -> JSON action -> policy
// validation) without a network call.
type Mock struct {
	// Reply, when set, is returned verbatim.
	Reply string
	Fail  bool
	// Delay simulates a slow local model for latency tests.
	Delay time.Duration
}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Available(ctx context.Context) bool { return true }

func (m *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	if m.Delay > 0 {
		select {
		case <-time.After(m.Delay):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	if m.Fail {
		return Response{}, fmt.Errorf("mock provider failure")
	}
	text := m.Reply
	if text == "" {
		// Pick the first action key from the prompt's ACTIONS block.
		text = `{"kind":"wait","reason":"mock policy"}`
		if i := strings.Index(req.Prompt, "ACTIONS:"); i >= 0 {
			line := req.Prompt[i+len("ACTIONS:"):]
			if j := strings.IndexByte(line, '\n'); j >= 0 {
				line = line[:j]
			}
			line = strings.TrimSpace(line)
			var actions []map[string]any
			if json.Unmarshal([]byte(line), &actions) == nil && len(actions) > 0 {
				if k, ok := actions[0]["kind"].(string); ok {
					b, _ := json.Marshal(map[string]any{"kind": k, "reason": "mock policy"})
					text = string(b)
				}
			}
		}
	}
	return Response{
		Text:             text,
		Provider:         "mock",
		Model:            "mock",
		PromptTokens:     len(req.Prompt) / 4,
		CompletionTokens: len(text) / 4,
		Latency:          2 * time.Millisecond,
	}, nil
}
