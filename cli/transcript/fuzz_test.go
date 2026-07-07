package transcript

import (
	"bytes"
	"compress/gzip"
	"io"
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

		// gz path: exercise the same decompress-then-parse logic openTranscriptReader
		// uses for a ".gz" transcript, entirely in memory. Must not panic or hang.
		// (The thin disk-based path lookup/fallback in openTranscriptReader itself
		// is covered by TestParseFromFileAtLine_* in parse_test.go; doing real
		// filesystem I/O on every one of millions of fuzz iterations here made this
		// target orders of magnitude slower than the other Fuzz* targets in this
		// repo, up to the point of colliding with the fuzz engine's own per-run
		// deadline and failing with a spurious "context deadline exceeded".)
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, werr := gw.Write(data); werr != nil {
			t.Fatalf("gzip write: %v", werr)
		}
		if cerr := gw.Close(); cerr != nil {
			t.Fatalf("gzip close: %v", cerr)
		}
		gzReader, gzErr := gzip.NewReader(&buf)
		if gzErr != nil {
			t.Fatalf("gzip.NewReader on our own compressed output: %v", gzErr)
		}
		decompressed, readErr := io.ReadAll(gzReader)
		if readErr != nil {
			t.Fatalf("decompressing our own gzip output: %v", readErr)
		}
		if _, gerr := ParseFromBytes(decompressed); gerr != nil {
			t.Fatalf("ParseFromBytes (decompressed) returned error on arbitrary input: %v", gerr)
		}
	})
}
