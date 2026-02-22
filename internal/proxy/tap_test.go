package proxy

import (
	"io"
	"strings"
	"testing"

	"github.com/ggmolly/claude-gate/testutil"
)

func TestTappingReader_NormalSSEStream(t *testing.T) {
	events := testutil.StandardSSEEvents(100, 42)
	body := testutil.BuildSSEResponse(events)

	var got UsageData
	var completed bool
	tr := newTappingReader(io.NopCloser(strings.NewReader(body)), func(u UsageData) {
		got = u
		completed = true
	})

	// Read entire body.
	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	// Body must pass through unchanged.
	if string(out) != body {
		t.Errorf("body mismatch:\ngot:  %q\nwant: %q", string(out), body)
	}

	if !completed {
		t.Fatal("onComplete was not called")
	}

	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", got.InputTokens)
	}
	if got.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", got.OutputTokens)
	}
	if got.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-sonnet-4-20250514")
	}
}

func TestTappingReader_PartialChunks(t *testing.T) {
	events := testutil.StandardSSEEvents(50, 20)
	body := testutil.BuildSSEResponse(events)

	var got UsageData
	tr := newTappingReader(io.NopCloser(strings.NewReader(body)), func(u UsageData) {
		got = u
	})

	// Read one byte at a time to simulate partial chunks.
	var all []byte
	buf := make([]byte, 1)
	for {
		n, err := tr.Read(buf)
		if n > 0 {
			all = append(all, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	if string(all) != body {
		t.Errorf("body mismatch after byte-by-byte read")
	}
	if got.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", got.InputTokens)
	}
	if got.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", got.OutputTokens)
	}
}

func TestTappingReader_NonSSE(t *testing.T) {
	body := `{"type":"message","content":[{"text":"hello"}]}`

	var got UsageData
	var completed bool
	tr := newTappingReader(io.NopCloser(strings.NewReader(body)), func(u UsageData) {
		got = u
		completed = true
	})

	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(out) != body {
		t.Errorf("body mismatch")
	}
	if !completed {
		t.Fatal("onComplete should still be called on EOF")
	}
	// No SSE lines, so usage should be zero.
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("expected zero usage for non-SSE, got input=%d output=%d", got.InputTokens, got.OutputTokens)
	}
}

func TestTappingReader_EmptyBody(t *testing.T) {
	var completed bool
	tr := newTappingReader(io.NopCloser(strings.NewReader("")), func(u UsageData) {
		completed = true
	})

	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty body, got %d bytes", len(out))
	}
	if !completed {
		t.Fatal("onComplete should be called even for empty body")
	}
}

func TestTappingReader_ErrorEvent(t *testing.T) {
	// Simulates an error event from the API.
	events := []testutil.SSEEvent{
		{
			Event: "error",
			Data:  `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		},
	}
	body := testutil.BuildSSEResponse(events)

	var got UsageData
	tr := newTappingReader(io.NopCloser(strings.NewReader(body)), func(u UsageData) {
		got = u
	})

	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(out) != body {
		t.Errorf("body mismatch")
	}
	// Error events have no usage data.
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("expected zero usage for error event, got input=%d output=%d", got.InputTokens, got.OutputTokens)
	}
}

func TestTappingReader_CacheTokens(t *testing.T) {
	events := []testutil.SSEEvent{
		{
			Event: "message_start",
			Data:  `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"cache_creation_input_tokens":500,"cache_read_input_tokens":200,"output_tokens":1}}}`,
		},
		{
			Event: "message_delta",
			Data:  `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}`,
		},
		{
			Event: "message_stop",
			Data:  `{"type":"message_stop"}`,
		},
	}
	body := testutil.BuildSSEResponse(events)

	var got UsageData
	tr := newTappingReader(io.NopCloser(strings.NewReader(body)), func(u UsageData) {
		got = u
	})

	_, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if got.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", got.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != 200 {
		t.Errorf("CacheReadInputTokens = %d, want 200", got.CacheReadInputTokens)
	}
	if got.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", got.InputTokens)
	}
	if got.OutputTokens != 30 {
		t.Errorf("OutputTokens = %d, want 30", got.OutputTokens)
	}
}
