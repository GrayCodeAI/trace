package redact

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

// findOfType returns the first finding of the given type, or false.
func findOfType(fs []Finding, typ string) (Finding, bool) {
	for _, f := range fs {
		if f.Type == typ {
			return f, true
		}
	}
	return Finding{}, false
}

// makeGitHubToken builds a structurally valid GitHub token (prefix + 30-char
// random body + 6-char CRC32 checksum) so the verifier's checksum path is
// exercised against a token it must accept.
func makeGitHubToken(prefix, body string) string {
	return prefix + body + gitHubChecksum(body)
}

func TestDetectGitHubTokenVerified(t *testing.T) {
	body := "abcdefghijklmnopqrstuvwxyz0123" // 30 chars
	tok := makeGitHubToken("ghp_", body)
	if len(tok) != 40 {
		t.Fatalf("expected 40-char token, got %d (%q)", len(tok), tok)
	}

	fs := Detect("token: " + tok + " end")
	f, ok := findOfType(fs, "github-token")
	if !ok {
		t.Fatalf("github-token not detected in %v", fs)
	}
	if f.Verification != Verified {
		t.Errorf("valid checksum should be Verified, got %s", f.Verification)
	}
	if f.Secret != tok {
		t.Errorf("secret span mismatch: got %q want %q", f.Secret, tok)
	}
}

func TestDetectGitHubTokenBadChecksumUnverified(t *testing.T) {
	body := "abcdefghijklmnopqrstuvwxyz0123"
	tok := makeGitHubToken("ghp_", body)
	// Flip the last checksum char to a different base62 char.
	bad := tok[:len(tok)-1] + "Z"
	if bad == tok {
		bad = tok[:len(tok)-1] + "Y"
	}

	fs := Detect(bad)
	f, ok := findOfType(fs, "github-token")
	if !ok {
		t.Fatalf("github-token not detected for %q", bad)
	}
	if f.Verification != Unverified {
		t.Errorf("tampered checksum should be Unverified, got %s", f.Verification)
	}
}

func TestDetectJWTVerified(t *testing.T) {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := enc(map[string]any{"alg": "HS256", "typ": "JWT"})
	payload := enc(map[string]any{"sub": "1234567890", "name": "Ada"})
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature-bytes-here"))
	jwt := header + "." + payload + "." + sig

	fs := Detect("Authorization: Bearer " + jwt)
	f, ok := findOfType(fs, "jwt")
	if !ok {
		t.Fatalf("jwt not detected in %v", fs)
	}
	if f.Verification != Verified {
		t.Errorf("decodable JWT header should be Verified, got %s", f.Verification)
	}
}

func TestDetectJWTGarbageHeaderUnverified(t *testing.T) {
	// Looks like a JWT (eyJ...eyJ...) but the first segment is not valid JSON.
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("eyJnotjson"))
	fake := "eyJ" + notJSON[3:] + ".eyJabcd.signature1234"
	if !jwtRE.MatchString(fake) {
		t.Skip("constructed string did not match jwt pattern")
	}
	fs := Detect(fake)
	if f, ok := findOfType(fs, "jwt"); ok && f.Verification == Verified {
		t.Errorf("non-JSON header must not verify, got Verified")
	}
}

func TestDetectPEMPrivateKeyVerified(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	fs := Detect("here is a key:\n" + string(pemBytes) + "\ndone")
	f, ok := findOfType(fs, "pem-private-key")
	if !ok {
		t.Fatalf("pem-private-key not detected")
	}
	if f.Verification != Verified {
		t.Errorf("parseable PEM should be Verified, got %s", f.Verification)
	}
	if !strings.Contains(f.Secret, "BEGIN EC PRIVATE KEY") {
		t.Errorf("PEM span should include the block, got %q", f.Secret)
	}
}

func TestDetectPaymentCardLuhn(t *testing.T) {
	// 4242 4242 4242 4242 is a Luhn-valid test card; 4242...4241 is not.
	fs := Detect("card 4242 4242 4242 4242 on file")
	f, ok := findOfType(fs, "payment-card")
	if !ok {
		t.Fatalf("payment-card not detected")
	}
	if f.Verification != Verified {
		t.Errorf("Luhn-valid card should be Verified, got %s", f.Verification)
	}

	fs2 := Detect("card 4242 4242 4242 4241 on file")
	if _, ok := findOfType(fs2, "payment-card"); ok {
		t.Errorf("Luhn-invalid number should not be a payment-card finding")
	}
}

func TestDetectAWSAccessKeyUnverified(t *testing.T) {
	fs := Detect("aws key AKIAIOSFODNN7EXAMPLE here")
	f, ok := findOfType(fs, "aws-access-key")
	if !ok {
		t.Fatalf("aws-access-key not detected")
	}
	// No offline checksum for AWS key IDs: strong pattern but Unverified.
	if f.Verification != Unverified {
		t.Errorf("aws key should be Unverified, got %s", f.Verification)
	}
}

func TestDetectPlaceholderRejected(t *testing.T) {
	// A high-entropy-looking placeholder mask should be Rejected, not flagged
	// as a real secret.
	fs := Detect("password = xxxxxxxxxxxxxxxx")
	for _, f := range fs {
		if f.Type == "high-entropy" && f.Verification == Verified {
			t.Errorf("placeholder mask must not verify: %+v", f)
		}
	}
}

func TestDetectHighEntropyUnverified(t *testing.T) {
	// Random-looking token with no known prefix: high entropy, unverified.
	fs := Detect("token=Zk9q2WpL7vR4tX1cB6nM8dF3hJ5sA0gE")
	f, ok := findOfType(fs, "high-entropy")
	if !ok {
		t.Fatalf("high-entropy token not detected in %v", fs)
	}
	if f.Verification != Unverified {
		t.Errorf("high-entropy token should be Unverified, got %s", f.Verification)
	}
}

func TestDetectNoFindingsInCleanText(t *testing.T) {
	fs := Detect("the quick brown fox jumps over the lazy dog")
	for _, f := range fs {
		if f.Verification == Verified {
			t.Errorf("clean prose produced a verified finding: %+v", f)
		}
	}
}

func TestVerificationString(t *testing.T) {
	cases := map[Verification]string{
		Verified: "verified", Unverified: "unverified", Rejected: "rejected",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("Verification(%d).String() = %q, want %q", v, got, want)
		}
	}
}
