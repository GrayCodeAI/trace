package redact

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"hash/crc32"
	"regexp"
	"sort"
	"strings"
)

// Verification grades the confidence that a detected candidate is a real,
// usable credential. TruffleHog draws the same distinction between a pattern
// match and a *verified* secret; we do it offline (no network egress, no
// secret exfiltration) using checksums and structural decoding.
type Verification int

const (
	// Rejected: the candidate looks like a placeholder, example, or mask
	// ("REDACTED", "<password>", "xxxx", "${VAR}") — almost certainly not a
	// live secret.
	Rejected Verification = iota
	// Unverified: matches a known secret pattern or is high-entropy, but no
	// offline proof is available. Redacted, but lower triage priority.
	Unverified
	// Verified: structurally or cryptographically confirmed — a GitHub token
	// whose CRC32 checksum matches, a JWT whose header decodes, a PEM block
	// that parses, or a card number that passes Luhn.
	Verified
)

func (v Verification) String() string {
	switch v {
	case Verified:
		return "verified"
	case Unverified:
		return "unverified"
	case Rejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// Finding is a single detected secret candidate with its location, the kind of
// secret it appears to be, and how confidently it was verified.
//
// Redaction (String/Bytes) still masks every non-rejected candidate — Detect is
// the reporting/triage surface that lets callers surface the real leaks first.
type Finding struct {
	// Type is a stable identifier for the kind of secret, e.g. "github-token",
	// "jwt", "pem-private-key", "aws-access-key", "high-entropy", "pattern",
	// or "credential".
	Type string
	// Secret is the matched substring.
	Secret string
	// Start and End are byte offsets into the scanned string ([Start, End)).
	Start int
	End   int
	// Verification grades confidence the match is a live credential.
	Verification Verification
}

// typedVerifier recognizes a specific credential shape and grades it.
type typedVerifier struct {
	name    string
	pattern *regexp.Regexp
	// grade returns the verification level for a matched substring. A verifier
	// that matches the pattern but cannot prove the secret returns Unverified.
	grade func(match string) Verification
}

var (
	// GitHub tokens: prefix + 36 base62 chars (30 random + 6 CRC32 checksum).
	githubTokenRE = regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36}\b`)
	// JWT: three base64url segments; header begins with the encoding of `{"`.
	jwtRE = regexp.MustCompile(`\beyJ[0-9A-Za-z_-]{4,}\.eyJ[0-9A-Za-z_-]{4,}\.[0-9A-Za-z_-]{4,}\b`)
	// AWS access key IDs: a distinctive 4-char entity prefix + 16 base32 chars.
	awsAccessKeyRE = regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|A3T[0-9A-Z])[0-9A-Z]{16}\b`)
	// Stripe live/test secret & restricted keys.
	stripeKeyRE = regexp.MustCompile(`\b[rs]k_(?:live|test)_[0-9A-Za-z]{16,}\b`)
	// Google API keys.
	googleAPIKeyRE = regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)
	// Slack tokens.
	slackTokenRE = regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`)
	// PEM private key header (the verifier parses the surrounding block).
	pemHeaderRE = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
)

var typedVerifiers = []typedVerifier{
	{name: "github-token", pattern: githubTokenRE, grade: gradeGitHubToken},
	{name: "jwt", pattern: jwtRE, grade: gradeJWT},
	{name: "pem-private-key", pattern: pemHeaderRE, grade: func(string) Verification { return Verified }},
	{name: "aws-access-key", pattern: awsAccessKeyRE, grade: func(string) Verification { return Unverified }},
	{name: "stripe-key", pattern: stripeKeyRE, grade: func(string) Verification { return Unverified }},
	{name: "google-api-key", pattern: googleAPIKeyRE, grade: func(string) Verification { return Unverified }},
	{name: "slack-token", pattern: slackTokenRE, grade: func(string) Verification { return Unverified }},
}

// base62Alphabet matches GitHub's token checksum encoding (digits, then
// lowercase, then uppercase). See GitHub's "Behind GitHub's new authentication
// token formats" — the trailing 6 chars are a base62-encoded CRC32 of the
// random body, enabling offline false-positive-free detection.
const base62Alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// gitHubChecksum encodes a CRC32 (IEEE) value as a left-zero-padded 6-char
// base62 string, matching the checksum GitHub appends to its tokens.
func gitHubChecksum(body string) string {
	sum := uint64(crc32.ChecksumIEEE([]byte(body)))
	buf := []byte("000000")
	for i := len(buf) - 1; i >= 0; i-- {
		buf[i] = base62Alphabet[sum%62]
		sum /= 62
	}
	return string(buf)
}

// gradeGitHubToken verifies a GitHub token by recomputing the trailing CRC32
// checksum over the random body. A match is cryptographic proof of a
// well-formed token (Verified); a mismatch is still a strong pattern match
// (Unverified) so it is never silently dropped from redaction.
func gradeGitHubToken(match string) Verification {
	us := strings.IndexByte(match, '_')
	if us < 0 || len(match)-us-1 != 36 {
		return Unverified
	}
	payload := match[us+1:]
	body, checksum := payload[:30], payload[30:]
	if gitHubChecksum(body) == checksum {
		return Verified
	}
	return Unverified
}

// gradeJWT verifies a JWT by base64url-decoding the header and confirming it is
// a JSON object declaring an "alg". This proves real JWT structure rather than
// a coincidental dotted base64 string.
func gradeJWT(match string) Verification {
	parts := strings.Split(match, ".")
	if len(parts) != 3 {
		return Unverified
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Unverified
	}
	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		return Unverified
	}
	if _, ok := header["alg"]; ok {
		return Verified
	}
	return Unverified
}

// verifyPEMBlock attempts to parse a PEM private-key block starting at the
// given header offset, returning the full block range if it decodes.
func verifyPEMBlock(s string, headerStart int) (int, bool) {
	block, _ := pem.Decode([]byte(s[headerStart:]))
	if block == nil || !strings.Contains(block.Type, "PRIVATE KEY") {
		return 0, false
	}
	// Re-encode to measure the consumed length precisely.
	end := headerStart + len(pem.EncodeToMemory(block))
	if end > len(s) {
		end = len(s)
	}
	return end, true
}

// luhnValid reports whether a run of digits passes the Luhn checksum used by
// payment-card numbers.
func luhnValid(digits string) bool {
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	var sum int
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

var cardRE = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)

// Detect scans s for secret candidates and returns structured findings, each
// graded by how confidently it was verified offline. Findings are sorted by
// start offset; overlapping matches are de-duplicated keeping the most
// confident, most specific finding.
//
// Detect never performs network I/O: "verification" means checksum/structural
// proof, so secrets are never transmitted to confirm them.
func Detect(s string) []Finding {
	var findings []Finding

	// 1. Typed, verifiable credential shapes (highest specificity).
	for _, v := range typedVerifiers {
		if v.name == "pem-private-key" {
			for _, loc := range v.pattern.FindAllStringIndex(s, -1) {
				if end, ok := verifyPEMBlock(s, loc[0]); ok {
					findings = append(findings, Finding{
						Type: v.name, Secret: s[loc[0]:end],
						Start: loc[0], End: end, Verification: Verified,
					})
				}
			}
			continue
		}
		for _, loc := range v.pattern.FindAllStringIndex(s, -1) {
			match := s[loc[0]:loc[1]]
			findings = append(findings, Finding{
				Type: v.name, Secret: match,
				Start: loc[0], End: loc[1], Verification: v.grade(match),
			})
		}
	}

	// 2. Payment-card numbers verified via Luhn.
	for _, loc := range cardRE.FindAllStringIndex(s, -1) {
		match := s[loc[0]:loc[1]]
		digits := strings.NewReplacer(" ", "", "-", "").Replace(match)
		if luhnValid(digits) {
			findings = append(findings, Finding{
				Type: "payment-card", Secret: match,
				Start: loc[0], End: loc[1], Verification: Verified,
			})
		}
	}

	// 3. High-entropy tokens (entropy proves randomness, not liveness).
	for _, loc := range secretPattern.FindAllStringIndex(s, -1) {
		match := s[loc[0]:loc[1]]
		if shannonEntropy(match) <= entropyThreshold {
			continue
		}
		v := Unverified
		if isPlaceholderSecretValue(match) {
			v = Rejected
		}
		findings = append(findings, Finding{
			Type: "high-entropy", Secret: match,
			Start: loc[0], End: loc[1], Verification: v,
		})
	}

	// 4. Vendor pattern library (betterleaks) — pattern match, unverified.
	if d := getDetector(); d != nil {
		for _, f := range d.DetectString(s) {
			if f.Secret == "" {
				continue
			}
			searchFrom := 0
			for {
				idx := strings.Index(s[searchFrom:], f.Secret)
				if idx < 0 {
					break
				}
				abs := searchFrom + idx
				findings = append(findings, Finding{
					Type: "pattern", Secret: f.Secret,
					Start: abs, End: abs + len(f.Secret), Verification: Unverified,
				})
				searchFrom = abs + len(f.Secret)
			}
		}
	}

	// 5. Credentialed URIs and connection strings — unverified credentials.
	for _, loc := range credentialedURIPattern.FindAllStringIndex(s, -1) {
		findings = append(findings, Finding{
			Type: "credential", Secret: s[loc[0]:loc[1]],
			Start: loc[0], End: loc[1], Verification: Unverified,
		})
	}
	for _, r := range detectConnectionStrings(s) {
		findings = append(findings, Finding{
			Type: "credential", Secret: s[r.start:r.end],
			Start: r.start, End: r.end, Verification: Unverified,
		})
	}
	for _, r := range detectCredentialValues(s) {
		findings = append(findings, Finding{
			Type: "credential", Secret: s[r.start:r.end],
			Start: r.start, End: r.end, Verification: Unverified,
		})
	}

	return dedupeFindings(findings)
}

// dedupeFindings sorts findings by start offset and removes any finding fully
// contained in another. When two findings share the same span, the more
// confident (then more specific/typed) one wins.
func dedupeFindings(in []Finding) []Finding {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Start != in[j].Start {
			return in[i].Start < in[j].Start
		}
		if in[i].End != in[j].End {
			return in[i].End > in[j].End // wider span first
		}
		if in[i].Verification != in[j].Verification {
			return in[i].Verification > in[j].Verification // more confident first
		}
		return typeSpecificity(in[i].Type) > typeSpecificity(in[j].Type)
	})
	out := []Finding{in[0]}
	for _, f := range in[1:] {
		last := &out[len(out)-1]
		// Drop findings fully covered by the previously kept (wider) span.
		if f.Start >= last.Start && f.End <= last.End {
			// If the contained finding is strictly more confident, promote it.
			if f.Start == last.Start && f.End == last.End && f.Verification > last.Verification {
				*last = f
			}
			continue
		}
		out = append(out, f)
	}
	return out
}

// typeSpecificity ranks typed detectors above generic ones for tie-breaking.
func typeSpecificity(t string) int {
	switch t {
	case "high-entropy", "pattern":
		return 0
	case "credential":
		return 1
	default:
		return 2 // named credential types (github-token, jwt, ...)
	}
}
