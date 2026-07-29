package redact

import (
	"context"
	"strings"
	"testing"
)

func configureFakeOPF(t *testing.T, fake *fakeRuntime, cats map[string]bool) {
	t.Helper()
	resetOPFConfig()
	t.Cleanup(resetOPFConfig)
	ConfigurePrivacyFilterWithRuntime(OPFConfig{
		Enabled:    true,
		Categories: cats,
	}, fake)
}

func TestBatchBytesWithPrivacyFilter_BatchesSingleCall(t *testing.T) {
	fake := &fakeRuntime{spans: []Span{{Start: 0, End: 5, Label: "private_person"}}}
	configureFakeOPF(t, fake, map[string]bool{"private_person": true})

	inputs := []NamedBlob{
		{Name: "full.jsonl", Content: []byte(`{"content":"Alice met Bob"}` + "\n" + `{"content":"Charlie sat down"}`)},
		{Name: "metadata.json", Content: []byte(`{"summary":"Eve walked home"}`)},
		{Name: "prompt.txt", Content: []byte("Frank reviewed the diff")},
	}

	_, err := BatchBytesWithPrivacyFilter(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchBytesWithPrivacyFilter: %v", err)
	}
	if fake.batchCalls != 1 {
		t.Errorf("want exactly 1 RedactBatch call across %d blobs, got %d", len(inputs), fake.batchCalls)
	}
	if fake.calls != 0 {
		t.Errorf("want 0 single-input Redact calls, got %d", fake.calls)
	}
}

func TestBatchBytesWithPrivacyFilter_PreservesInputOrder(t *testing.T) {
	fake := &fakeRuntime{}
	configureFakeOPF(t, fake, map[string]bool{"private_person": true})

	inputs := []NamedBlob{
		{Name: "a.txt", Content: []byte("alpha content here")},
		{Name: "b.txt", Content: []byte("beta content here")},
		{Name: "c.txt", Content: []byte("gamma content here")},
	}

	got, err := BatchBytesWithPrivacyFilter(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchBytesWithPrivacyFilter: %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("want %d outputs, got %d", len(inputs), len(got))
	}
	wantPrefixes := []string{"alpha", "beta", "gamma"}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(string(got[i]), prefix) {
			t.Errorf("output[%d] = %q, want prefix %q", i, string(got[i]), prefix)
		}
	}
}

func TestBatchBytesWithPrivacyFilter_AppliesSpansToJSON(t *testing.T) {
	fake := &fakeRuntime{spans: []Span{{Start: 0, End: 5, Label: "private_person"}}}
	configureFakeOPF(t, fake, map[string]bool{"private_person": true})

	inputs := []NamedBlob{
		{Name: "metadata.json", Content: []byte(`{"summary":"Alice met Bob","id":"keep-this"}`)},
	}
	got, err := BatchBytesWithPrivacyFilter(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchBytesWithPrivacyFilter: %v", err)
	}
	out := string(got[0])
	if !strings.Contains(out, "[REDACTED_PERSON]") {
		t.Errorf("expected [REDACTED_PERSON] tag, got %q", out)
	}
	if !strings.Contains(out, `"keep-this"`) {
		t.Errorf("non-prose id field should survive, got %q", out)
	}
}

func TestBatchBytesWithPrivacyFilter_AppliesSpansToRawText(t *testing.T) {
	fake := &fakeRuntime{spans: []Span{{Start: 0, End: 5, Label: "private_person"}}}
	configureFakeOPF(t, fake, map[string]bool{"private_person": true})

	inputs := []NamedBlob{
		{Name: "prompt.txt", Content: []byte("Alice met Bob in the lobby")},
	}
	got, err := BatchBytesWithPrivacyFilter(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchBytesWithPrivacyFilter: %v", err)
	}
	if !strings.Contains(string(got[0]), "[REDACTED_PERSON]") {
		t.Errorf("expected [REDACTED_PERSON] in raw-text output, got %q", string(got[0]))
	}
}

type recordingRuntime struct {
	spans      []Span
	lastInputs []string
	calls      int
}

func (r *recordingRuntime) Redact(_ context.Context, _ string, _ []string) ([]Span, error) {
	return r.spans, nil
}

func (r *recordingRuntime) RedactBatch(_ context.Context, inputs []string, _ []string) ([][]Span, error) {
	r.calls++
	r.lastInputs = append([]string(nil), inputs...)
	out := make([][]Span, len(inputs))
	for i := range inputs {
		out[i] = r.spans
	}
	return out, nil
}

func TestBatchBytesWithPrivacyFilter_DedupsLeavesAcrossBlobs(t *testing.T) {
	rt := &recordingRuntime{spans: []Span{{Start: 0, End: 5, Label: "private_person"}}}
	resetOPFConfig()
	t.Cleanup(resetOPFConfig)
	ConfigurePrivacyFilterWithRuntime(OPFConfig{
		Enabled:    true,
		Categories: map[string]bool{"private_person": true},
	}, rt)

	shared := "Alice met Bob in the lobby"
	inputs := []NamedBlob{
		{Name: "a.jsonl", Content: []byte(`{"text":"` + shared + `"}`)},
		{Name: "b.jsonl", Content: []byte(`{"text":"` + shared + `"}`)},
		{Name: "c.txt", Content: []byte(shared)},
	}
	_, err := BatchBytesWithPrivacyFilter(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchBytesWithPrivacyFilter: %v", err)
	}
	if rt.calls != 1 {
		t.Errorf("want 1 batch call, got %d", rt.calls)
	}
	if got := len(rt.lastInputs); got != 1 {
		t.Errorf("want dedup to collapse 3 identical leaves into 1 batch input, got %d (inputs: %v)", got, rt.lastInputs)
	}
	if len(rt.lastInputs) == 1 && rt.lastInputs[0] != shared {
		t.Errorf("want dedup'd input = %q, got %q", shared, rt.lastInputs[0])
	}
}

func TestBatchBytesWithPrivacyFilter_EmptyInputs(t *testing.T) {
	fake := &fakeRuntime{}
	configureFakeOPF(t, fake, map[string]bool{"private_person": true})

	got, err := BatchBytesWithPrivacyFilter(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil inputs: %v", err)
	}
	if got != nil {
		t.Errorf("want nil output for nil input, got %v", got)
	}
	if fake.batchCalls != 0 {
		t.Errorf("want 0 OPF calls for nil input, got %d", fake.batchCalls)
	}
}
