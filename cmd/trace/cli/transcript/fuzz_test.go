package transcript

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseFromBytes ensures the transcript parser never panics on arbitrary
// input and that parsing is deterministic across the byte and gzip code paths.
//
// The parser is security-relevant: it consumes untrusted JSONL transcripts
// produced by external agents (Claude Code, Cursor, etc.), so malformed,
// truncated, or adversarial input must be handled gracefully.
func FuzzParseFromBytes(f *testing.F) {
	seeds := [][]byte{
		[]byte(``),
		[]byte("\n"),
		[]byte(`{"type":"user","uuid":"u1","message":{"content":"hello"}}` + "\n"),
		[]byte(`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"),
		[]byte(`{"role":"user","uuid":"u2","message":{"content":["x",{"type":"text","text":"y"}]}}`),
		[]byte("not json\n{broken\n{\"type\":\"user\"}\n"),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Non-gz path: must never panic and must not error on arbitrary bytes
		// (malformed lines are skipped, not surfaced as errors).
		lines, err := ParseFromBytes(data)
		if err != nil {
			t.Fatalf("ParseFromBytes returned error on arbitrary input: %v", err)
		}

		// Exercise downstream extraction on every parsed line — also must not panic.
		for i := range lines {
			_ = ExtractUserContent(lines[i].Message)
		}

		// SliceFromLine must never panic regardless of offset.
		_ = SliceFromLine(data, 0)
		_ = SliceFromLine(data, 1)
		_ = SliceFromLine(data, len(data)+1)

		// gz path: write the same bytes gzip-compressed and parse via the file
		// reader, which transparently decompresses the .gz variant. Must not panic.
		dir := t.TempDir()
		base := filepath.Join(dir, "transcript.jsonl")
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, werr := gw.Write(data); werr != nil {
			t.Fatalf("gzip write: %v", werr)
		}
		if cerr := gw.Close(); cerr != nil {
			t.Fatalf("gzip close: %v", cerr)
		}
		if werr := os.WriteFile(base+".gz", buf.Bytes(), 0o600); werr != nil {
			t.Fatalf("write gz file: %v", werr)
		}
		// base does not exist, so openTranscriptReader falls back to base+".gz".
		if _, gerr := ParseFromFileAtLine(base, 0); gerr != nil {
			t.Fatalf("ParseFromFileAtLine (gz) returned error: %v", gerr)
		}
	})
}
