package redact

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// highEntropySecret is a string with Shannon entropy > 4.5 that will trigger redaction.
const highEntropySecret = "sk-ant-api03-xK9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6pA"

// Test constants for repeated string literals (goconst).
const (
	testFieldContent   = "content"
	testFieldSessionID = "session_id"
	testFieldFilePath  = "file_path"
	testFieldCwd       = "cwd"
	testFieldType      = "type"
	testFieldText      = "text"

	testSessionID = "ses_37273a1fdffegpYbwUTqEkPsQ0"

	wantRedacted           = "REDACTED"
	wantDBPasswordRedacted = "DB_PASSWORD=REDACTED"
	wantConnRedacted       = "conn=REDACTED"
	wantDBURLRedacted      = "DATABASE_URL=REDACTED"

	testPathTmpE2E          = "/tmp/TestE2E_Something3407889464/001/controller.go"
	testPathPrivateVar      = "/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/TestE2E_Something/controller"
	testPathUserClaude      = "/Users/peytonmontei/.claude/projects/something.jsonl"
	testJSONEscapeNewline   = `controller.go\nmodel.go\nview.go`
	testJSONEscapeTab       = `something.go\tanother.go`
	testJSONEscapeBackslash = `C:\\Users\\test\\file.go`

	testLabelEmployeeID = "EMPLOYEE_ID"

	testPathMultilineFiles = "/tmp/test/controller.go\n/tmp/test/model.go\n/tmp/test/view.go"
)

var fakeOpenSSHPrivateKey = makeFakeOpenSSHPrivateKey(`b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACB7ZlJ8tkWCKdRJRGF1BngP3bkNbz8bMF6Yl5xLJp9m1QAAAJj2M3UO9jN1
DgAAAAtzc2gtZWQyNTUxOQAAACB7ZlJ8tkWCKdRJRGF1BngP3bkNbz8bMF6Yl5xLJp9m1QA
AAEAGZmFrZS1rZXktZm9yLXJlZGFjdGlvbi10ZXN0LW9ubHkBAgMEBQY=`)

func makeFakeOpenSSHPrivateKey(payload string) string {
	return strings.Join([]string{
		openSSHPrivateKeyMarker("BEGIN"),
		payload,
		openSSHPrivateKeyMarker("END"),
	}, "\n")
}

func openSSHPrivateKeyMarker(kind string) string {
	return "-----" + kind + " " + "OPEN" + "SSH" + " " + "PRIVATE" + " KEY-----"
}

type stringRedactionCase struct {
	name  string
	input string
	want  string
}

func assertStringRedactionCases(t *testing.T, tests []stringRedactionCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := String(tt.input)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBytes_NoSecrets(t *testing.T) {
	input := []byte("hello world, this is normal text")
	result := Bytes(input)
	if string(result) != string(input) {
		t.Errorf("expected unchanged input, got %q", result)
	}
	// Should return the original slice when no changes
	if &result[0] != &input[0] {
		t.Error("expected same underlying slice when no redaction needed")
	}
}

func TestBytes_WithSecret(t *testing.T) {
	input := []byte("my key is " + highEntropySecret + " ok")
	result := Bytes(input)
	expected := []byte("my key is REDACTED ok")
	if !bytes.Equal(result, expected) {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestJSONLBytes_NoSecrets(t *testing.T) {
	input := []byte(`{"type":"text","content":"hello"}`)
	result, err := JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Bytes()) != string(input) {
		t.Errorf("expected unchanged input, got %q", result.Bytes())
	}
	if &result.Bytes()[0] != &input[0] {
		t.Error("expected same underlying slice when no redaction needed")
	}
}

func TestJSONLBytes_WithSecret(t *testing.T) {
	input := []byte(`{"type":"text","content":"key=` + highEntropySecret + `"}`)
	result, err := JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []byte(`{"type":"text","content":"` + wantRedacted + `"}`)
	if !bytes.Equal(result.Bytes(), expected) {
		t.Errorf("got %q, want %q", result.Bytes(), expected)
	}
}

func TestRedactedBytes_Bytes(t *testing.T) {
	t.Parallel()
	input := []byte(`{"type":"text","content":"hello"}`)
	rb := AlreadyRedacted(input)
	if !bytes.Equal(rb.Bytes(), input) {
		t.Errorf("Bytes() = %q, want %q", rb.Bytes(), input)
	}
}

func TestRedactedBytes_Len(t *testing.T) {
	t.Parallel()
	input := []byte(`some data`)
	rb := AlreadyRedacted(input)
	if rb.Len() != len(input) {
		t.Errorf("Len() = %d, want %d", rb.Len(), len(input))
	}
}

func TestAlreadyRedacted(t *testing.T) {
	t.Parallel()
	input := []byte(`some data`)
	rb := AlreadyRedacted(input)
	if !bytes.Equal(rb.Bytes(), input) {
		t.Errorf("AlreadyRedacted() = %q, want %q", rb.Bytes(), input)
	}
}

func TestJSONLContent_TopLevelArray(t *testing.T) {
	// Top-level JSON arrays are valid JSONL and should be redacted.
	input := `["` + highEntropySecret + `","normal text"]`
	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := `["` + wantRedacted + `","normal text"]`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestJSONLContent_TopLevelArrayNoSecrets(t *testing.T) {
	input := `["hello","world"]`
	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != input {
		t.Errorf("expected unchanged input, got %q", result)
	}
}

func TestJSONLContent_MultipleObjects_AllRedacted(t *testing.T) {
	t.Parallel()
	// Regression test: JSONL with multiple top-level JSON objects must redact
	// secrets in ALL objects, not just the first. The single-JSON fast path must
	// not accidentally consume only the first object and return early.
	input := `{"content":"safe text","id":"abc"}
{"content":"key=` + highEntropySecret + `","id":"def"}
{"content":"also safe","id":"ghi"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The secret in the second line should be redacted.
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in second JSONL object was not redacted")
	}
	if !strings.Contains(result, wantRedacted) {
		t.Error("expected REDACTED in output")
	}

	// IDs should be preserved (field-aware skip).
	for _, id := range []string{"abc", "def", "ghi"} {
		if !strings.Contains(result, id) {
			t.Errorf("ID %q should be preserved", id)
		}
	}

	// Non-secret content should be preserved.
	if !strings.Contains(result, "safe text") {
		t.Error("safe text in first object was corrupted")
	}
	if !strings.Contains(result, "also safe") {
		t.Error("safe text in third object was corrupted")
	}
}

func TestJSONLContent_InvalidJSONLine(t *testing.T) {
	// Lines that aren't valid JSON should be processed with normal string redaction.
	input := `{"type":"text", "invalid ` + highEntropySecret + " json"
	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := `{"type":"text", "invalid REDACTED json`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestCollectJSONLReplacements_Succeeds(t *testing.T) {
	obj := map[string]any{
		testFieldContent: "token=" + highEntropySecret,
	}
	repls := collectJSONLReplacements(obj)
	// expect one replacement for high-entropy secret
	want := []jsonReplacement{{key: testFieldContent, original: "token=" + highEntropySecret, redacted: wantRedacted}}
	if !slices.Equal(repls, want) {
		t.Errorf("got %q, want %q", repls, want)
	}
}

func TestShouldSkipJSONLField(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		// Fields ending in "id" should be skipped.
		{"id", true},
		{testFieldSessionID, true},
		{"sessionId", true},
		{"checkpoint_id", true},
		{"checkpointID", true},
		{"userId", true},
		// Fields ending in "ids" should be skipped.
		{"ids", true},
		{testFieldSessionID + "s", true},
		{"userIds", true},
		// Exact match "signature" should be skipped.
		{"signature", true},
		// Path-related fields should be skipped.
		{"filePath", true},
		{testFieldFilePath, true},
		{testFieldCwd, true},
		{"root", true},
		{"directory", true},
		{"dir", true},
		{"path", true},
		// Fields that should NOT be skipped.
		{testFieldContent, false},
		{testFieldType, false},
		{"name", false},
		{testFieldText, false},
		{"output", false},
		{"input", false},
		{"command", false},
		{"args", false},
		{"video", false},      // ends in "o", not "id"
		{"identify", false},   // ends in "ify", not "id"
		{"signatures", false}, // not exact match "signature"
		{"signal_data", false},
		{"consideration", false}, // contains "id" but doesn't end with it
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := shouldSkipJSONLField(tt.key)
			if got != tt.want {
				t.Errorf("shouldSkipJSONLField(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestShouldSkipJSONLField_RedactionBehavior(t *testing.T) {
	// Verify that secrets in skipped fields are preserved (not redacted).
	obj := map[string]any{
		testFieldSessionID: highEntropySecret,
		testFieldContent:   highEntropySecret,
	}
	repls := collectJSONLReplacements(obj)
	// Only "content" should produce a replacement; "session_id" should be skipped.
	if len(repls) != 1 {
		t.Fatalf("expected 1 replacement, got %d", len(repls))
	}
	if repls[0].original != highEntropySecret {
		t.Errorf("expected replacement for secret in content field, got %q", repls[0].original)
	}
}

func TestJSONLContent_SkippedFieldValueCollision(t *testing.T) {
	t.Parallel()
	input := `{"` + testFieldSessionID + `":"` + highEntropySecret + `","` + testFieldContent + `":"` + highEntropySecret + `"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, `"session_id":"`+highEntropySecret+`"`) {
		t.Fatalf("expected skipped session_id to be preserved, got: %s", result)
	}
	if !strings.Contains(result, `"`+testFieldContent+`":"`+wantRedacted+`"`) {
		t.Fatalf("expected content field to be redacted, got: %s", result)
	}
}

func TestString_PatternDetection(t *testing.T) {
	// These secrets have entropy below 4.5 so entropy-only detection misses them.
	// Betterleaks pattern matching should catch them.
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "AWS access key (entropy ~3.9, below 4.5 threshold)",
			input: "key=AKIAYRWQG5EJLPZLBYNP",
			want:  "key=" + wantRedacted,
		},
		{
			name:  "two AWS keys separated by space produce two REDACTED tokens",
			input: "key=AKIAYRWQG5EJLPZLBYNP AKIAYRWQG5EJLPZLBYNP",
			want:  "key=" + wantRedacted + " " + wantRedacted,
		},
		{
			name:  "adjacent AWS keys without separator merge into single REDACTED",
			input: "key=AKIAYRWQG5EJLPZLBYNPAKIAYRWQG5EJLPZLBYNP",
			want:  "key=" + wantRedacted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify entropy is below threshold (proving entropy-only would miss this).
			for _, loc := range secretPattern.FindAllStringIndex(tt.input, -1) {
				e := shannonEntropy(tt.input[loc[0]:loc[1]])
				if e > entropyThreshold {
					t.Fatalf("test secret has entropy %.2f > %.1f; this test is meant for low-entropy secrets", e, entropyThreshold)
				}
			}

			got := String(tt.input)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestString_CredentialedURIs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "postgres URI",
			input: "DATABASE_URL=postgres://app:pwd123@db.example.com:5432/app",
			want:  wantDBURLRedacted,
		},
		{
			name:  "postgresql URI with query",
			input: `dsn="postgresql://svc:moderatepw@localhost/app?sslmode=require"`,
			want:  `dsn="` + wantRedacted + `"`,
		},
		{
			name:  "mongodb srv URI",
			input: "mongo=mongodb+srv://user:pass123@cluster0.example.mongodb.net/app?retryWrites=true",
			want:  "mongo=REDACTED",
		},
		{
			name:  "mysql URI",
			input: "mysql://root:p@localhost:3306/app",
			want:  wantRedacted,
		},
		{
			name:  "redis URI with empty username",
			input: "cache redis://:hunter2@localhost:6379/0",
			want:  "cache " + wantRedacted,
		},
		{
			name:  "generic credentialed URL",
			input: "proxy=https://user:pass@example.com/path",
			want:  "proxy=" + wantRedacted,
		},
		{
			name:  "URL without password is preserved",
			input: "repo=ssh://git@github.com/GrayCodeAI/trace",
			want:  "repo=ssh://git@github.com/GrayCodeAI/trace",
		},
		{
			name:  "colon and at-sign in path are preserved",
			input: "url=https://example.com/a:b@c",
			want:  "url=https://example.com/a:b@c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.input)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestString_DatabaseConnectionStringRedaction(t *testing.T) {
	t.Parallel()
	assertStringRedactionCases(t, []stringRedactionCase{
		{
			name:  "postgres keyword DSN",
			input: `dsn="host=db.example.com port=5432 user=svc password=secret dbname=app sslmode=require"`,
			want:  `dsn="` + wantRedacted + `"`,
		},
		{
			name:  "postgres keyword DSN different order",
			input: "password=secret sslmode=require user=svc host=db.example.com dbname=app",
			want:  wantRedacted,
		},
		{
			name:  "sql server connection string",
			input: "conn=Server=tcp:db.example.com,1433;Database=app;User Id=svc;Password=secret;Encrypt=true",
			want:  wantConnRedacted,
		},
		{
			name:  "odbc connection string",
			input: "conn=Driver={ODBC Driver 18 for SQL Server};Server=db;UID=svc;PWD=secret;Database=app",
			want:  wantConnRedacted,
		},
		{
			name:  "jdbc query password",
			input: "jdbc:postgresql://db.example.com:5432/app?user=svc&password=secret&ssl=true",
			want:  wantRedacted,
		},
		{
			name:  "postgres URL query password without userinfo",
			input: "DATABASE_URL=postgresql://db.example.com:5432/app?user=svc&password=secret&sslmode=require",
			want:  "DATABASE_URL=REDACTED",
		},
		{
			name:  "postgres URL query password is case-insensitive",
			input: "DATABASE_URL=postgresql://db.example.com:5432/app?user=svc&Password=secret&sslmode=require",
			want:  "DATABASE_URL=REDACTED",
		},
		{
			name:  "mongodb URL query password without userinfo",
			input: "MONGO_URL=mongodb://cluster0.example.mongodb.net/app?authSource=admin&username=svc&password=secret",
			want:  "MONGO_URL=REDACTED",
		},
		{
			name:  "mongodb srv URL query password without userinfo",
			input: "MONGO_URL=mongodb+srv://cluster0.example.mongodb.net/app?authSource=admin&username=svc&password=secret",
			want:  "MONGO_URL=REDACTED",
		},
		{
			name:  "placeholder password in database URL query is preserved",
			input: "DATABASE_URL=postgresql://db.example.com/app?user=svc&password=${DB_PASSWORD}",
			want:  "DATABASE_URL=postgresql://db.example.com/app?user=svc&password=${DB_PASSWORD}",
		},
		{
			name:  "jdbc semicolon password",
			input: "jdbc:sqlserver://db.example.com:1433;databaseName=app;user=svc;password=secret;encrypt=true",
			want:  wantRedacted,
		},
		{
			name:  "ado.net quoted password with embedded semicolons",
			input: `conn=Server=db.example.com;User ID=svc;Password="se;cret;here";Encrypt=true`,
			want:  wantConnRedacted,
		},
		{
			name:  "ado.net single-quoted password with embedded semicolons",
			input: `conn=Server=db.example.com;User ID=svc;Password='se;cret;here';Encrypt=true`,
			want:  wantConnRedacted,
		},
	})
}

func TestDatabaseConnectionStringRuleScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		candidate string
		hasSecret func(string) bool
		want      bool
	}{
		{
			name:      "database URL query password is in scope",
			candidate: "postgresql://db.example.com:5432/app?user=svc&password=secret&sslmode=require",
			hasSecret: hasDatabaseURLSecret,
			want:      true,
		},
		{
			name:      "database URL userinfo password is handled by credentialed URI detection",
			candidate: "postgresql://svc:secret@db.example.com:5432/app",
			hasSecret: hasDatabaseURLSecret,
			want:      false,
		},
		{
			name:      "JDBC query password is in scope",
			candidate: "jdbc:postgresql://db.example.com:5432/app?user=svc&password=secret",
			hasSecret: hasJDBCPassword,
			want:      true,
		},
		{
			name:      "JDBC userinfo password is handled by credentialed URI detection",
			candidate: "jdbc:postgresql://svc:secret@db.example.com:5432/app",
			hasSecret: hasJDBCPassword,
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.hasSecret(tt.candidate)
			if got != tt.want {
				t.Errorf("hasSecret(%q) = %v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}

func TestString_BoundedCredentialValueRedaction(t *testing.T) {
	t.Parallel()
	assertStringRedactionCases(t, []stringRedactionCase{
		{
			name:  "db password env var",
			input: "DB_PASSWORD=secret123",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "postgres password env var",
			input: "PGPASSWORD='secret123'",
			want:  "PGPASSWORD='REDACTED'",
		},
		{
			name:  "redis password env var",
			input: `REDIS_PASSWORD="secret123"`,
			want:  `REDIS_PASSWORD="` + wantRedacted + `"`,
		},
		{
			name:  "lowercase database password",
			input: "database_password=secret123",
			want:  "database_password=REDACTED",
		},
		{
			name:  "prefixed db password env var",
			input: "APP_DB_PASSWORD=secret123",
			want:  "APP_DB_PASSWORD=REDACTED",
		},
		{
			name:  "prefixed mysql password env var",
			input: "PROD_MYSQL_PWD=secret123",
			want:  "PROD_MYSQL_PWD=REDACTED",
		},
		{
			name:  "mysql root password env var",
			input: "MYSQL_ROOT_PASSWORD=secret123",
			want:  "MYSQL_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mariadb root password env var",
			input: "MARIADB_ROOT_PASSWORD=secret123",
			want:  "MARIADB_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mongo initdb root password env var",
			input: "MONGO_INITDB_ROOT_PASSWORD=secret123",
			want:  "MONGO_INITDB_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mssql sa password env var",
			input: "MSSQL_SA_PASSWORD=secret123",
			want:  "MSSQL_SA_PASSWORD=REDACTED",
		},
		{
			name:  "double underscore separator",
			input: "DB__PASSWORD=secret123",
			want:  "DB__PASSWORD=REDACTED",
		},
	})
}

func TestString_BoundedCredentialValueOverRedactionGuards(t *testing.T) {
	t.Parallel()
	assertStringRedactionCases(t, []stringRedactionCase{
		{
			name:  "placeholder env var is preserved",
			input: "DB_PASSWORD=${DB_PASSWORD}",
			want:  "DB_PASSWORD=${DB_PASSWORD}",
		},
		{
			name:  "already redacted value is preserved",
			input: "DB_PASSWORD=REDACTED",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "prose about password is preserved",
			input: "the password field should be rotated regularly",
			want:  "the password field should be rotated regularly",
		},
		{
			name:  "generic key is preserved",
			input: "key=not-a-secret-setting",
			want:  "key=not-a-secret-setting",
		},
		{
			name:  "shell pwd is preserved",
			input: "PWD=/workspace/project",
			want:  "PWD=/workspace/project",
		},
		{
			name:  "standalone password assignment is preserved",
			input: "password=not-a-secret-setting",
			want:  "password=not-a-secret-setting",
		},
		{
			name:  "password reset query parameter is preserved",
			input: "https://example.com/?password_reset=true",
			want:  "https://example.com/?password_reset=true",
		},
		{
			name:  "generic https password query is preserved",
			input: "https://example.com/callback?user=svc&password=not-a-db-credential&debug=true",
			want:  "https://example.com/callback?user=svc&password=not-a-db-credential&debug=true",
		},
		{
			name:  "db password hash field is preserved",
			input: "DB_PASSWORD_HASH=abcdef",
			want:  "DB_PASSWORD_HASH=abcdef",
		},
		{
			name:  "non-credential mysql field is preserved",
			input: "MYSQL_USER_ID=alice",
			want:  "MYSQL_USER_ID=alice",
		},
		{
			name:  "angle bracket placeholder is preserved",
			input: "DB_PASSWORD=<password>",
			want:  "DB_PASSWORD=<password>",
		},
		{
			name:  "your_password placeholder is preserved",
			input: "DB_PASSWORD=your_password",
			want:  "DB_PASSWORD=your_password",
		},
		{
			name:  "your-db-password placeholder is preserved",
			input: "DB_PASSWORD=<your-db-password>",
			want:  "DB_PASSWORD=<your-db-password>",
		},
		{
			name:  "asterisk mask placeholder is preserved",
			input: "DB_PASSWORD=*****",
			want:  "DB_PASSWORD=*****",
		},
		{
			name:  "dot mask placeholder is preserved",
			input: "DB_PASSWORD=......",
			want:  "DB_PASSWORD=......",
		},
		{
			name:  "secret_here placeholder is preserved",
			input: "DB_PASSWORD=secret_here",
			want:  "DB_PASSWORD=secret_here",
		},
		{
			name:  "placeholder literal is preserved",
			input: "DB_PASSWORD=placeholder",
			want:  "DB_PASSWORD=placeholder",
		},
	})
}

// Pins that single-char "masks" and arbitrary <…> wrappers do NOT count as
// placeholders, so credentials that happen to be short or bracket-wrapped
// still get redacted. The opposite cases (`***`, `<password>`, etc.) are
// covered above in TestString_BoundedCredentialValueOverRedactionGuards.
func TestString_ShortAndOpaquePlaceholdersFallThrough(t *testing.T) {
	t.Parallel()
	assertStringRedactionCases(t, []stringRedactionCase{
		{
			name:  "single x is not a mask",
			input: "DB_PASSWORD=x",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "single dash is not a mask",
			input: "DB_PASSWORD=-",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "single asterisk is not a mask",
			input: "DB_PASSWORD=*",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "two-char repeat is not a mask",
			input: "DB_PASSWORD=xx",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "bracketed value with digits is not a placeholder",
			input: "DB_PASSWORD=<hunter2>",
			want:  wantDBPasswordRedacted,
		},
		{
			name:  "bracketed mixed-case value is not a placeholder",
			input: "DB_PASSWORD=<RealPassword>",
			want:  wantDBPasswordRedacted,
		},
	})
}

func TestString_OpenSSHPrivateKeyBlock(t *testing.T) {
	input := "key:\n" + fakeOpenSSHPrivateKey + "\nend"
	want := "key:\nREDACTED\nend"

	got := String(input)
	if got != want {
		t.Errorf("String(private key block) = %q, want %q", got, want)
	}
	if strings.Contains(got, openSSHPrivateKeyMarker("BEGIN")) || strings.Contains(got, openSSHPrivateKeyMarker("END")) {
		t.Errorf("private key block markers should be fully redacted, got %q", got)
	}
}

func TestJSONLContent_CredentialedURI(t *testing.T) {
	input := `{"type":"text","content":"DATABASE_URL=postgres://app:pwd123@db.example.com:5432/app"}`
	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, "postgres://app:pwd123@db.example.com:5432/app") {
		t.Error("credentialed database URI was not redacted")
	}
	if !strings.Contains(result, "DATABASE_URL=REDACTED") {
		t.Errorf("expected credentialed URI replacement, got %q", result)
	}
}

func TestJSONLContent_OpenSSHPrivateKeyBlock(t *testing.T) {
	content, err := json.Marshal("key:\n" + fakeOpenSSHPrivateKey + "\nend")
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	input := `{"type":"text","content":` + string(content) + `}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, openSSHPrivateKeyMarker("BEGIN")) || strings.Contains(result, openSSHPrivateKeyMarker("END")) {
		t.Errorf("private key block markers should be fully redacted, got %q", result)
	}
	if !strings.Contains(result, `key:\nREDACTED\nend`) {
		t.Errorf("expected whole private key block replacement, got %q", result)
	}
}

func TestJSONLContent_DatabaseCredentialRedaction(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","message":"dsn host=db.example.com user=svc password=secret dbname=app and env DB_PASSWORD=secret123","session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0","file_path":"/tmp/TestE2E_ExistingFiles/controller.go"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, leaked := range []string{"password=secret", "DB_PASSWORD=secret123"} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected %q to be redacted, got: %s", leaked, result)
		}
	}
	for _, preserved := range []string{testSessionID, "/tmp/TestE2E_ExistingFiles/controller.go"} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected structural value %q to be preserved, got: %s", preserved, result)
		}
	}
}
