package transcript

import (
	"bytes"
	"strconv"
	"testing"
)

// buildBenchTranscript produces a synthetic JSONL transcript with n lines,
// alternating user and assistant messages, for benchmarking the parser.
func buildBenchTranscript(n int) []byte {
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i)
		if i%2 == 0 {
			buf.WriteString(`{"type":"user","uuid":"u` + id + `","message":{"content":"prompt number ` + id + ` with some text"}}` + "\n")
		} else {
			buf.WriteString(`{"type":"assistant","uuid":"a` + id + `","message":{"content":[{"type":"text","text":"reply ` + id + `"},{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}` + "\n")
		}
	}
	return buf.Bytes()
}

// BenchmarkParseFromBytes measures the hot path of parsing a JSONL transcript,
// which runs on every checkpoint/summarize operation.
func BenchmarkParseFromBytes(b *testing.B) {
	content := buildBenchTranscript(500)
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lines, err := ParseFromBytes(content)
		if err != nil {
			b.Fatal(err)
		}
		if len(lines) == 0 {
			b.Fatal("expected parsed lines")
		}
	}
}
