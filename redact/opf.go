package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// OPFConfig configures the optional OpenAI Privacy Filter detection layer.
// Defaults are applied by ConfigurePrivacyFilter; callers should pass values
// straight from settings without local normalization.
type OPFConfig struct {
	Enabled    bool
	Categories map[string]bool
	Command    string // path or name of the opf binary; "" defaults to "opf"
	Timeout    int    // seconds; 0 defaults to 30

	// runtime is private and constructed by ConfigurePrivacyFilter.
	// Tests inject a fake via ConfigurePrivacyFilterWithRuntime.
	runtime opfRuntime
}

var (
	opfConfig   *OPFConfig
	opfConfigMu sync.RWMutex

	// opfBreakerTripped, once set, disables OPF for the rest of this process.
	// The first detectOPF failure trips it via handleOPFFailure; subsequent
	// detectOPF calls short-circuit before shelling out. Without this, a
	// broken OPF install (binary missing, persistently timing out, etc.)
	// makes every redaction call pay the full timeout — turning one
	// condensation into N × 30s waits instead of a single warning plus
	// graceful fallback. Process-scoped so a fresh CLI invocation re-attempts.
	opfBreakerTripped atomic.Bool
)

// ConfigurePrivacyFilter sets the global OPF configuration and constructs
// the default shell-out runtime. Call once at process startup after loading
// settings. Thread-safe. Subsequent calls replace the previous configuration
// and reset the circuit breaker (a new config might fix what broke the prior
// one).
func ConfigurePrivacyFilter(cfg OPFConfig) {
	cfgCopy := cfg
	if cfgCopy.Timeout <= 0 {
		cfgCopy.Timeout = 30
	}
	if cfgCopy.Command == "" {
		cfgCopy.Command = defaultOPFCommand
	}
	cfgCopy.runtime = newShellOut(cfgCopy.Command, cfgCopy.Timeout)
	opfConfigMu.Lock()
	opfConfig = &cfgCopy
	opfConfigMu.Unlock()
	opfBreakerTripped.Store(false)
}

// ConfigurePrivacyFilterWithRuntime is the test-only variant that takes an
// explicit runtime instead of constructing one.
func ConfigurePrivacyFilterWithRuntime(cfg OPFConfig, rt opfRuntime) {
	cfgCopy := cfg
	if cfgCopy.Timeout <= 0 {
		cfgCopy.Timeout = 30
	}
	cfgCopy.runtime = rt
	opfConfigMu.Lock()
	opfConfig = &cfgCopy
	opfConfigMu.Unlock()
	opfBreakerTripped.Store(false)
}

func getOPFConfig() *OPFConfig {
	opfConfigMu.RLock()
	defer opfConfigMu.RUnlock()
	return opfConfig
}

// OPFEnabled reports whether the OpenAI Privacy Filter is configured
// and turned on for this process. Callers gate pre-push rewrite work
// on this: when false, the pre-push hook pushes the local regex-only
// checkpoint branch verbatim with no extra processing. Independent of
// the circuit breaker — a tripped breaker still reports Enabled=true
// because the runtime config didn't change; the rewrite logic itself
// handles the breaker by short-circuiting per-commit OPF calls.
func OPFEnabled() bool {
	cfg := getOPFConfig()
	return cfg != nil && cfg.Enabled
}

// OPFBreakerTripped reports whether the per-process OPF circuit breaker
// has been tripped — i.e. an OPF invocation failed at some point during
// this process's lifetime.
func OPFBreakerTripped() bool {
	return opfBreakerTripped.Load()
}

// defaultOPFCommand is the binary name we resolve via $PATH when the
// user hasn't pinned a specific path in settings. Used as the fallback
// inside ConfigurePrivacyFilter and as the OPFCommand() default for
// error messages.
const defaultOPFCommand = "opf"

// OPFCommand returns the configured OPF binary command, or the default
// when OPF is unconfigured. Used by error messages so the user sees the
// exact command they need to fix.
func OPFCommand() string {
	cfg := getOPFConfig()
	if cfg == nil || cfg.Command == "" {
		return defaultOPFCommand
	}
	return cfg.Command
}

func resetOPFConfig() {
	opfConfigMu.Lock()
	opfConfig = nil
	opfConfigMu.Unlock()
	opfBreakerTripped.Store(false)
}

// opfLabelMap maps OPF native labels to our taggedRegion.label values.
// Empty mapped value renders as the bare "REDACTED" token; non-empty values
// render as "[REDACTED_<LABEL>]" via the replacementToken helper.
var opfLabelMap = map[string]string{
	"private_person":  "PERSON",
	"private_email":   "EMAIL",
	"private_phone":   "PHONE",
	"private_address": "ADDRESS",
	"private_url":     "URL",
	"private_date":    "DATE",
	"account_number":  "ACCOUNT_NUMBER",
	"secret":          "", // -> bare REDACTED
}

// IsKnownOPFCategory reports whether name is one of the OPF native labels
// the CLI knows how to tag and render. Exported so the settings layer can
// reject typos at parse time.
func IsKnownOPFCategory(name string) bool {
	_, ok := opfLabelMap[name]
	return ok
}

func mapOPFLabel(opfLabel string) string {
	if mapped, ok := opfLabelMap[opfLabel]; ok {
		return mapped
	}
	return ""
}

func enabledCategories(cfg *OPFConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Categories))
	for label, enabled := range cfg.Categories {
		if !enabled {
			continue
		}
		if _, known := opfLabelMap[label]; !known {
			continue
		}
		out = append(out, label)
	}
	sort.Strings(out) // deterministic order for tests + logs
	return out
}

var opfStderr io.Writer = os.Stderr

// detectOPF runs OPF on a single text and returns tagged regions for any
// spans whose category is enabled in cfg.
func detectOPF(ctx context.Context, cfg *OPFConfig, s string) []taggedRegion {
	if cfg == nil || !cfg.Enabled || cfg.runtime == nil || s == "" {
		return nil
	}
	if opfBreakerTripped.Load() {
		return nil
	}
	if !strings.ContainsRune(s, ' ') {
		return nil
	}
	cats := enabledCategories(cfg)
	if len(cats) == 0 {
		return nil
	}

	_, _ = fmt.Fprintln(opfStderr, "→ OpenAI Privacy Filter: scanning transcript…")
	start := time.Now()
	batched, err := cfg.runtime.RedactBatch(ctx, []string{s}, cats)
	if err != nil {
		handleOPFFailure(ctx, cfg, err)
		return nil
	}
	if len(batched) != 1 {
		handleOPFFailure(ctx, cfg, fmt.Errorf("opf runtime returned %d span slices for 1 input", len(batched)))
		return nil
	}
	spans := batched[0]
	_, _ = fmt.Fprintf(opfStderr, "✓ OpenAI Privacy Filter: done (%.1fs)\n", time.Since(start).Seconds())

	return opfSpanRegions(s, spans, cfg)
}

func opfSpanRegions(v string, spans []Span, cfg *OPFConfig) []taggedRegion {
	out := make([]taggedRegion, 0, len(spans))
	for _, sp := range spans {
		if !cfg.Categories[sp.Label] {
			continue
		}
		if sp.Start < 0 || sp.End > len(v) || sp.Start >= sp.End {
			continue
		}
		out = append(out, taggedRegion{
			region: region{sp.Start, sp.End},
			label:  mapOPFLabel(sp.Label),
		})
	}
	return out
}

func handleOPFFailure(ctx context.Context, cfg *OPFConfig, err error) {
	if !opfBreakerTripped.CompareAndSwap(false, true) {
		return
	}
	slog.WarnContext(
		ctx, "OpenAI Privacy Filter call failed; disabling for the rest of this process",
		slog.String("component", "redaction"),
		slog.String("command", cfg.Command),
		slog.String("error", err.Error()),
	)
	_, _ = fmt.Fprintf(opfStderr, "× OpenAI Privacy Filter unavailable (%s); falling back to regex layers for the rest of this commit. Install with 'pip install opf'.\n", cfg.Command)
}

type Span struct {
	Start int
	End   int
	Label string // OPF native label, e.g. "private_person"
}

type opfRuntime interface {
	Redact(ctx context.Context, text string, categories []string) ([]Span, error)
	RedactBatch(ctx context.Context, inputs []string, categories []string) ([][]Span, error)
}

type shellOut struct {
	command        string
	timeoutSeconds int
	commandRunner  func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func newShellOut(command string, timeoutSeconds int) *shellOut {
	return &shellOut{
		command:        command,
		timeoutSeconds: timeoutSeconds,
		commandRunner:  exec.CommandContext,
	}
}

const (
	opfBatchSeparator        = "\x1e"
	opfMaxBatchInputBytes    = 16 * 1024 * 1024
	opfMaxProcessOutputBytes = 1 * 1024 * 1024
)

func (s *shellOut) Redact(ctx context.Context, text string, categories []string) ([]Span, error) {
	if len(categories) == 0 {
		return nil, nil
	}
	batch, err := s.RedactBatch(ctx, []string{text}, categories)
	if err != nil {
		return nil, err
	}
	if len(batch) != 1 {
		return nil, fmt.Errorf("opf runtime returned %d span slices for 1 input", len(batch))
	}
	return batch[0], nil
}

func (s *shellOut) RedactBatch(ctx context.Context, inputs []string, categories []string) ([][]Span, error) {
	if len(inputs) == 0 || len(categories) == 0 {
		return nil, nil
	}
	batchedLen, tooLarge := opfBatchedInputLen(inputs)
	if tooLarge {
		return nil, fmt.Errorf("opf input too large (%d bytes, limit %d)", batchedLen, opfMaxBatchInputBytes)
	}
	timeout := time.Duration(s.timeoutSeconds) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var buf strings.Builder
	buf.Grow(batchedLen)
	starts := make([]int, len(inputs))
	for i, in := range inputs {
		if i > 0 {
			buf.WriteString(opfBatchSeparator)
		}
		starts[i] = buf.Len()
		buf.WriteString(sanitizeOPFBatchInput(in))
	}
	batched := buf.String()

	cmd := s.commandRunner(
		callCtx, s.command,
		"--device", "cpu",
		"--output-mode", "typed",
		"--format", "json",
		"--no-print-color-coded-text",
	)
	cmd.Stdin = strings.NewReader(batched)
	stdout := newLimitedOutputBuffer(opfMaxProcessOutputBytes)
	stderr := newLimitedOutputBuffer(opfMaxProcessOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 500 * time.Millisecond

	if err := cmd.Run(); err != nil {
		if stdout.Exceeded() {
			return nil, fmt.Errorf("opf stdout exceeded %d byte limit", stdout.Limit())
		}
		if stderr.Exceeded() {
			return nil, fmt.Errorf("opf stderr exceeded %d byte limit", stderr.Limit())
		}
		switch {
		case errors.Is(callCtx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("opf timeout after %s: %w", timeout, callCtx.Err())
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, fmt.Errorf("opf canceled: %w", ctx.Err())
		}
		if stderr.Len() == 0 {
			return nil, fmt.Errorf("opf exited with error: %w", err)
		}
		return nil, fmt.Errorf("opf exited with error (%d bytes on stderr): %w", stderr.Len(), err)
	}
	if stdout.Exceeded() {
		return nil, fmt.Errorf("opf stdout exceeded %d byte limit", stdout.Limit())
	}
	if stderr.Exceeded() {
		return nil, fmt.Errorf("opf stderr exceeded %d byte limit", stderr.Limit())
	}

	var parsed struct {
		DetectedSpans []struct {
			Label string `json:"label"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		} `json:"detected_spans"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("opf output not parseable as JSON (%d bytes): %w", stdout.Len(), err)
	}

	out := make([][]Span, len(inputs))
	for _, p := range parsed.DetectedSpans {
		byteStart := charToByteOffset(batched, p.Start)
		byteEnd := charToByteOffset(batched, p.End)
		if byteStart < 0 || byteEnd < 0 {
			continue
		}
		idx := partitionIndex(starts, byteStart, byteEnd, opfBatchSeparator)
		if idx < 0 {
			continue
		}
		base := starts[idx]
		out[idx] = append(out[idx], Span{
			Start: byteStart - base,
			End:   byteEnd - base,
			Label: p.Label,
		})
	}
	return out, nil
}

func opfBatchedInputLen(inputs []string) (int, bool) {
	total := 0
	for i, in := range inputs {
		if i > 0 {
			if total > opfMaxBatchInputBytes-len(opfBatchSeparator) {
				return total + len(opfBatchSeparator), true
			}
			total += len(opfBatchSeparator)
		}
		if total > opfMaxBatchInputBytes-len(in) {
			return total + len(in), true
		}
		total += len(in)
	}
	return total, false
}

type limitedOutputBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func newLimitedOutputBuffer(limit int) *limitedOutputBuffer {
	return &limitedOutputBuffer{limit: limit}
}

func (b *limitedOutputBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
			return len(p), nil
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	b.exceeded = true
	return len(p), nil
}

func (b *limitedOutputBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedOutputBuffer) Len() int {
	return b.buf.Len()
}

func (b *limitedOutputBuffer) Limit() int {
	return b.limit
}

func (b *limitedOutputBuffer) Exceeded() bool {
	return b.exceeded
}

func sanitizeOPFBatchInput(in string) string {
	replacer := strings.NewReplacer("\n", " ", opfBatchSeparator, " ")
	return replacer.Replace(in)
}

func charToByteOffset(s string, charOff int) int {
	if charOff < 0 {
		return -1
	}
	byteOff := 0
	for range charOff {
		if byteOff >= len(s) {
			return -1
		}
		_, size := utf8.DecodeRuneInString(s[byteOff:])
		byteOff += size
	}
	return byteOff
}

func partitionIndex(starts []int, spanStart, spanEnd int, sep string) int {
	if spanStart < 0 || spanEnd <= spanStart {
		return -1
	}
	for i := range starts {
		base := starts[i]
		var end int
		if i+1 < len(starts) {
			end = starts[i+1] - len(sep)
		} else {
			end = 1 << 31
		}
		if spanStart >= base && spanEnd <= end {
			return i
		}
		if spanStart < base {
			return -1
		}
	}
	return -1
}
