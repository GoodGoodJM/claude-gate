package proxy

import (
	"bytes"
	"encoding/json"
	"io"
)

// UsageData holds token usage extracted from an SSE stream.
type UsageData struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	Model                    string `json:"model"`
}

// tappingReader wraps an io.ReadCloser, streaming bytes through while scanning
// for SSE data lines that contain usage information. When the underlying reader
// returns io.EOF, the onComplete callback fires with collected usage data.
type tappingReader struct {
	rc         io.ReadCloser
	usage      UsageData
	onComplete func(UsageData)
	lineBuf    []byte // partial line buffer for lines that span Read() calls
}

func newTappingReader(rc io.ReadCloser, onComplete func(UsageData)) *tappingReader {
	return &tappingReader{
		rc:         rc,
		onComplete: onComplete,
	}
}

func (t *tappingReader) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.scan(p[:n])
	}
	if err == io.EOF && t.onComplete != nil {
		t.onComplete(t.usage)
		t.onComplete = nil // fire only once
	}
	return n, err
}

func (t *tappingReader) Close() error {
	return t.rc.Close()
}

// scan processes a chunk of bytes, splitting by newlines and inspecting
// each complete line for SSE data containing usage information.
func (t *tappingReader) scan(chunk []byte) {
	// Prepend any leftover partial line from the previous Read.
	if len(t.lineBuf) > 0 {
		chunk = append(t.lineBuf, chunk...)
		t.lineBuf = nil
	}

	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			// No newline found; buffer the rest for next Read.
			t.lineBuf = append(t.lineBuf[:0], chunk...)
			return
		}
		line := chunk[:idx]
		chunk = chunk[idx+1:]
		t.processLine(line)
	}
}

var (
	sseDataPrefix        = []byte("data: ")
	sseEventMessageStart = []byte("message_start")
	sseEventMessageDelta = []byte("message_delta")
)

// processLine checks a single SSE line for usage data.
func (t *tappingReader) processLine(line []byte) {
	// Only look at SSE data lines.
	if !bytes.HasPrefix(line, sseDataPrefix) {
		return
	}
	payload := line[len(sseDataPrefix):]

	// Fast path: skip JSON parsing unless the line is relevant.
	hasStart := bytes.Contains(payload, sseEventMessageStart)
	hasDelta := bytes.Contains(payload, sseEventMessageDelta)
	if !hasStart && !hasDelta {
		return
	}

	if hasStart {
		t.parseMessageStart(payload)
	} else {
		t.parseMessageDelta(payload)
	}
}

// usageFields holds common token-usage counters shared across SSE and JSON envelopes.
type usageFields struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// messageStartEnvelope is the minimal structure for "message_start" events.
type messageStartEnvelope struct {
	Message struct {
		Model string      `json:"model"`
		Usage usageFields `json:"usage"`
	} `json:"message"`
}

func (t *tappingReader) parseMessageStart(data []byte) {
	var env messageStartEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	t.usage.Model = env.Message.Model
	t.usage.InputTokens = env.Message.Usage.InputTokens
	t.usage.CacheCreationInputTokens = env.Message.Usage.CacheCreationInputTokens
	t.usage.CacheReadInputTokens = env.Message.Usage.CacheReadInputTokens
}

// messageDeltaEnvelope is the minimal structure for "message_delta" events.
type messageDeltaEnvelope struct {
	Usage usageFields `json:"usage"`
}

func (t *tappingReader) parseMessageDelta(data []byte) {
	var env messageDeltaEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	t.usage.OutputTokens = env.Usage.OutputTokens
}
