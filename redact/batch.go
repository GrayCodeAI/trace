package redact

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
)

type NamedBlob struct {
	Name    string
	Content []byte
}

func BatchBytesWithPrivacyFilter(ctx context.Context, inputs []NamedBlob) ([][]byte, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	cfg := getOPFConfig()
	if cfg == nil || !cfg.Enabled || cfg.runtime == nil || opfBreakerTripped.Load() {
		return applyRegexLayersToBlobs(inputs), nil
	}
	cats := enabledCategories(cfg)
	if len(cats) == 0 {
		return applyRegexLayersToBlobs(inputs), nil
	}

	seen := make(map[string]struct{})
	var batchInputs []string
	addLeaf := func(v string) {
		if !strings.ContainsRune(v, ' ') {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		batchInputs = append(batchInputs, v)
	}
	for _, in := range inputs {
		collectLeaves(in, addLeaf)
	}

	spansByInput := make(map[string][]Span, len(batchInputs))
	if len(batchInputs) > 0 {
		_, _ = fmt.Fprintln(opfStderr, "→ OpenAI Privacy Filter: scanning checkpoints…")
		start := time.Now()
		batched, err := cfg.runtime.RedactBatch(ctx, batchInputs, cats)
		if err != nil {
			handleOPFFailure(ctx, cfg, err)
			return nil, fmt.Errorf("opf batch failed across %d blobs: %w", len(inputs), err)
		}
		_, _ = fmt.Fprintf(opfStderr, "✓ OpenAI Privacy Filter: done (%.1fs, %d blobs)\n",
			time.Since(start).Seconds(), len(inputs))
		if len(batched) != len(batchInputs) {
			shortErr := fmt.Errorf("opf runtime returned %d span slices for %d inputs", len(batched), len(batchInputs))
			handleOPFFailure(ctx, cfg, shortErr)
			return nil, fmt.Errorf("opf batch short return: %w", shortErr)
		}
		for i, leaf := range batchInputs {
			spansByInput[leaf] = batched[i]
		}
	}

	out := make([][]byte, len(inputs))
	for i, in := range inputs {
		out[i] = applyToBlob(in, spansByInput, cfg)
	}
	return out, nil
}

func collectLeaves(in NamedBlob, add func(string)) {
	if isJSONLikeName(in.Name) {
		if _, err := jsonlContentImpl(string(in.Content), func(v string) string {
			add(v)
			return v
		}); err == nil {
			return
		}
	}
	add(string(in.Content))
}

func applyToBlob(in NamedBlob, spansByInput map[string][]Span, cfg *OPFConfig) []byte {
	applier := func(v string) string {
		regions := detectAllLayers(v)
		regions = append(regions, opfSpanRegions(v, spansByInput[v], cfg)...)
		return applyRegions(v, regions)
	}
	if isJSONLikeName(in.Name) {
		if redacted, err := jsonlContentImpl(string(in.Content), applier); err == nil {
			return []byte(redacted)
		}
	}
	return []byte(applier(string(in.Content)))
}

func applyRegexLayersToBlobs(inputs []NamedBlob) [][]byte {
	out := make([][]byte, len(inputs))
	for i, in := range inputs {
		if isJSONLikeName(in.Name) {
			if redacted, err := jsonlContentImpl(string(in.Content), String); err == nil {
				out[i] = []byte(redacted)
				continue
			}
		}
		out[i] = []byte(String(string(in.Content)))
	}
	return out
}

func isJSONLikeName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".json")
}

func SumProseLeafBytes(inputs []NamedBlob) int {
	var total int
	for _, in := range inputs {
		if isJSONLikeName(in.Name) {
			if _, err := jsonlContentImpl(string(in.Content), func(v string) string {
				if strings.ContainsRune(v, ' ') {
					total += len(v)
				}
				return v
			}); err == nil {
				continue
			}
		}
		if bytes.ContainsRune(in.Content, ' ') {
			total += len(in.Content)
		}
	}
	return total
}
