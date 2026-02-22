package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggmolly/claude-gate/internal/store"
)

// NewTestStore creates a temporary SQLite store for testing.
// Uses a temp file because the store opens separate read/write connections,
// which don't share state with :memory: databases.
func NewTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		os.Remove(dbPath)
	})
	return s
}

// SSEEvent represents a single SSE event for building mock responses.
type SSEEvent struct {
	Event string
	Data  string
}

// BuildSSEResponse creates a valid SSE response body from events.
func BuildSSEResponse(events []SSEEvent) string {
	var result strings.Builder
	for _, e := range events {
		if e.Event != "" {
			result.WriteString("event: " + e.Event + "\n")
		}
		result.WriteString("data: " + e.Data + "\n\n")
	}
	return result.String()
}

// StandardSSEEvents returns a typical Claude API SSE event sequence.
func StandardSSEEvents(inputTokens, outputTokens int) []SSEEvent {
	return []SSEEvent{
		{
			Event: "message_start",
			Data:  fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":%d,"output_tokens":1}}}`, inputTokens),
		},
		{
			Event: "content_block_start",
			Data:  `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			Event: "content_block_delta",
			Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		},
		{
			Event: "content_block_stop",
			Data:  `{"type":"content_block_stop","index":0}`,
		},
		{
			Event: "message_delta",
			Data:  fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d}}`, outputTokens),
		},
		{
			Event: "message_stop",
			Data:  `{"type":"message_stop"}`,
		},
	}
}

// NewMockUpstream creates a test server that simulates Claude API SSE responses.
func NewMockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		events := StandardSSEEvents(25, 15)
		for _, e := range events {
			if e.Event != "" {
				fmt.Fprintf(w, "event: %s\n", e.Event)
			}
			fmt.Fprintf(w, "data: %s\n\n", e.Data)
			flusher.Flush()
		}
	}))
}

// NewMockUpstreamJSON creates a test server that returns a non-streaming JSON response.
func NewMockUpstreamJSON(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "msg_test",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-20250514",
			"content": []map[string]any{
				{"type": "text", "text": "Hello"},
			},
			"usage": map[string]any{
				"input_tokens":  25,
				"output_tokens": 15,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}
