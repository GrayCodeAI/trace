package redact

import (
	"slices"
	"strings"
	"testing"
)

func TestJSONLContent_StructuredCredentialFieldsRedacted(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","env":{"DB_PASSWORD":"correct-horse-db","REDIS_PASSWORD":"${REDIS_PASSWORD}","note":"correct-horse-db"},"db":{"password":"correct-horse-db","host":"db.example.com","user":"svc"},"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, leaked := range []string{`"DB_PASSWORD":"correct-horse-db"`, `"password":"correct-horse-db"`} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected structured credential field %q to be redacted, got: %s", leaked, result)
		}
	}
	for _, preserved := range []string{
		`"DB_PASSWORD":"` + wantRedacted + `"`,
		`"REDIS_PASSWORD":"${REDIS_PASSWORD}"`,
		`"password":"` + wantRedacted + `"`,
		`"host":"db.example.com"`,
		`"user":"svc"`,
		`"note":"correct-horse-db"`,
		testSessionID,
	} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected %q to be preserved, got: %s", preserved, result)
		}
	}
}

func TestJSONLContent_NormalizedCredentialKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","env":{"DB Password":"correct-horse-db","note":"correct-horse-db"},"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, preserved := range []string{
		`"DB Password":"` + wantRedacted + `"`,
		`"note":"correct-horse-db"`,
		testSessionID,
	} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected %q to be preserved, got: %s", preserved, result)
		}
	}
	if strings.Contains(result, `"DB Password":"correct-horse-db"`) {
		t.Fatalf("expected normalized credential key to be redacted, got: %s", result)
	}
}

func TestJSONLContent_DottedCredentialKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"config":{"db.password":"correct-horse-db","mysql.root.password":"correct-horse-mysql","note":"correct-horse-db"}}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, redacted := range []string{
		`"db.password":"` + wantRedacted + `"`,
		`"mysql.root.password":"` + wantRedacted + `"`,
	} {
		if !strings.Contains(result, redacted) {
			t.Fatalf("expected %q in output, got: %s", redacted, result)
		}
	}
	if !strings.Contains(result, `"note":"correct-horse-db"`) {
		t.Fatalf("expected unrelated note field to be preserved, got: %s", result)
	}
}

func TestJSONLContent_RootPasswordJSONKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"env":{"MYSQL_ROOT_PASSWORD":"correct-horse-mysql","MONGO_INITDB_ROOT_PASSWORD":"correct-horse-mongo","MSSQL_SA_PASSWORD":"correct-horse-mssql"}}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, redacted := range []string{
		`"MYSQL_ROOT_PASSWORD":"` + wantRedacted + `"`,
		`"MONGO_INITDB_ROOT_PASSWORD":"` + wantRedacted + `"`,
		`"MSSQL_SA_PASSWORD":"` + wantRedacted + `"`,
	} {
		if !strings.Contains(result, redacted) {
			t.Fatalf("expected %q in output, got: %s", redacted, result)
		}
	}
	for _, leaked := range []string{"correct-horse-mysql", "correct-horse-mongo", "correct-horse-mssql"} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected %q to be redacted, got: %s", leaked, result)
		}
	}
}

func TestShouldSkipJSONLObject(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{
			name: "image type is skipped",
			obj:  map[string]any{testFieldType: "image", "data": "base64data"},
			want: true,
		},
		{
			name: "text type is not skipped",
			obj:  map[string]any{testFieldType: testFieldText, testFieldContent: "hello"},
			want: false,
		},
		{
			name: "no type field is not skipped",
			obj:  map[string]any{testFieldContent: "hello"},
			want: false,
		},
		{
			name: "non-string type is not skipped",
			obj:  map[string]any{testFieldType: 42},
			want: false,
		},
		{
			name: "image_url type is skipped",
			obj:  map[string]any{testFieldType: "image_url"},
			want: true,
		},
		{
			name: "base64 type is skipped",
			obj:  map[string]any{testFieldType: "base64"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipJSONLObject(tt.obj)
			if got != tt.want {
				t.Errorf("shouldSkipJSONLObject(%v) = %v, want %v", tt.obj, got, tt.want)
			}
		})
	}
}

func TestShouldSkipJSONLObject_RedactionBehavior(t *testing.T) {
	// Verify that secrets inside image objects are NOT redacted.
	obj := map[string]any{
		testFieldType: "image",
		"data":        highEntropySecret,
	}
	repls := collectJSONLReplacements(obj)

	// expect no replacements, it's an image which is skipped.
	var wantRepls []jsonReplacement
	if !slices.Equal(repls, wantRepls) {
		t.Errorf("got %q, want %q", repls, wantRepls)
	}

	// Verify that secrets inside non-image objects ARE redacted.
	obj2 := map[string]any{
		testFieldType:    "text",
		testFieldContent: highEntropySecret,
	}
	repls2 := collectJSONLReplacements(obj2)
	wantRepls2 := []jsonReplacement{{key: testFieldContent, original: highEntropySecret, redacted: wantRedacted}}
	if !slices.Equal(repls2, wantRepls2) {
		t.Errorf("got %q, want %q", repls2, wantRepls2)
	}
}

func TestString_FilePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "temp directory path preserves filenames",
			input: testPathTmpE2E,
			want:  testPathTmpE2E,
		},
		{
			name:  "macOS private var folders path",
			input: testPathPrivateVar,
			want:  testPathPrivateVar,
		},
		{
			name:  "simple Go file path",
			input: "Reading file: /tmp/test/model.go",
			want:  "Reading file: /tmp/test/model.go",
		},
		{
			name:  "user home directory path",
			input: testPathUserClaude,
			want:  testPathUserClaude,
		},
		{
			name:  "multiple paths separated by newlines",
			input: testPathMultilineFiles,
			want:  testPathMultilineFiles,
		},
	}
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

func TestString_JSONEscapeSequences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "newline escape not corrupted",
			input: testJSONEscapeNewline,
			want:  testJSONEscapeNewline,
		},
		{
			name:  "tab escape not corrupted",
			input: testJSONEscapeTab,
			want:  testJSONEscapeTab,
		},
		{
			name:  "backslash escape not corrupted",
			input: testJSONEscapeBackslash,
			want:  testJSONEscapeBackslash,
		},
	}
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

func TestString_RealSecretsStillCaught(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "high entropy API key",
			input: "api_key=" + highEntropySecret,
		},
		{
			name:  "AWS access key (pattern-based)",
			input: "key=AKIAYRWQG5EJLPZLBYNP",
		},
		{
			name:  "GitHub personal access token",
			input: "token=ghp_1234567890abcdefghijklmnopqrstuvwxyzAB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := String(tt.input)
			if !strings.Contains(got, wantRedacted) {
				t.Errorf("String(%q) = %q, expected REDACTED somewhere", tt.input, got)
			}
		})
	}
}

func TestJSONLContent_PathFieldsPreserved(t *testing.T) {
	t.Parallel()
	// Simulates a real agent log line with path fields that should NOT be redacted
	input := `{"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0","file_path":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test/controller.go","cwd":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test","root":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test","directory":"/tmp/TestE2E_ExistingFiles","content":"normal text here"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Structural fields should be preserved
	mustContain := []string{
		testSessionID,                // session_id (skipped by *id rule)
		"/private/var/folders",       // file_path (skipped by path rule)
		"controller.go",              // filename in file_path
		"/tmp/TestE2E_ExistingFiles", // directory (skipped by path rule)
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected %q to be preserved, but result is: %s", s, result)
		}
	}

	// No false positives
	if strings.Contains(result, wantRedacted) {
		t.Errorf("expected no redactions in structural fields, got: %s", result)
	}
}

func TestJSONLContent_PrettyPrintedJSON_IDsPreserved(t *testing.T) {
	t.Parallel()
	// Simulates OpenCode's pretty-printed JSON export format.
	// High-entropy IDs (like msg_cb99a444f001Ftd3kTVmr8XQHZ with entropy > 4.5)
	// must be preserved. Before the fix, line-by-line processing couldn't parse
	// individual lines of pretty-printed JSON and fell back to entropy-based
	// redaction, corrupting these IDs.
	input := `{
  "info": {
    "id": "ses_309461a8bffeQfY7CYDOUHX6VP",
    "slug": "misty-river",
    "directory": "/tmp/test-repo"
  },
  "messages": [
    {
      "info": {
        "id": "msg_cb99a444f001Ftd3kTVmr8XQHZ",
        "sessionID": "ses_309461a8bffeQfY7CYDOUHX6VP",
        "role": "user"
      },
      "parts": [
        {
          "id": "prt_cb99a443b001GE99vjBG60vHbF",
          "type": "text",
          "text": "hello world"
        }
      ]
    },
    {
      "info": {
        "id": "msg_cb99a444f001Ftd3kTVmr8XQHZ",
        "sessionID": "ses_309461a8bffeQfY7CYDOUHX6VP",
        "role": "assistant"
      },
      "parts": [
        {
          "id": "prt_cb99a6f2e0012koCcOJBSwRBwR",
          "type": "text",
          "text": "hello back"
        },
        {
          "id": "prt_cb99a6f2f001e98CKuwDKU3oWr",
          "type": "tool",
          "tool": "write",
          "callID": "call_abc123",
          "state": {
            "status": "completed",
            "input": {"filePath": "/tmp/test/hello.md"},
            "output": "wrote file",
            "metadata": {"files": [{"filePath": "/tmp/test/hello.md", "relativePath": "hello.md"}]}
          }
        }
      ]
    }
  ]
}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the entropy threshold: msg_cb99a444f001Ftd3kTVmr8XQHZ has entropy > 4.5
	// and would be redacted by String() if processed line-by-line.
	entropy := shannonEntropy("msg_cb99a444f001Ftd3kTVmr8XQHZ")
	if entropy <= entropyThreshold {
		t.Fatalf("test assumption broken: msg ID entropy %.2f should be > %.1f", entropy, entropyThreshold)
	}

	// All IDs must be preserved (they're in "id"/"sessionID" fields which are skipped).
	mustContain := []string{
		"ses_309461a8bffeQfY7CYDOUHX6VP",
		"msg_cb99a444f001Ftd3kTVmr8XQHZ",
		"prt_cb99a443b001GE99vjBG60vHbF",
		"prt_cb99a6f2e0012koCcOJBSwRBwR",
		"prt_cb99a6f2f001e98CKuwDKU3oWr",
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected ID %q to be preserved, but it was corrupted in result", s)
		}
	}

	// No false positives on structural data.
	if strings.Contains(result, wantRedacted) {
		t.Errorf("expected no redactions in OpenCode export, got redacted content")
	}
}

func TestJSONLContent_PrettyPrintedJSON_SecretsStillCaught(t *testing.T) {
	t.Parallel()
	// Even in pretty-printed JSON mode, actual secrets in content fields should
	// still be redacted.
	input := `{
  "info": {
    "id": "ses_test123"
  },
  "messages": [
    {
      "info": {
        "id": "msg_test456",
        "role": "assistant"
      },
      "parts": [
        {
          "id": "prt_test789",
          "type": "text",
          "text": "your api key is ` + highEntropySecret + `"
        }
      ]
    }
  ]
}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Secret in text content should be redacted.
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in text field was not redacted")
	}
	if !strings.Contains(result, wantRedacted) {
		t.Error("expected REDACTED in output")
	}

	// IDs should still be preserved.
	for _, id := range []string{"ses_test123", "msg_test456", "prt_test789"} {
		if !strings.Contains(result, id) {
			t.Errorf("ID %q should be preserved", id)
		}
	}
}

func TestJSONLContent_SecretsInContentStillCaught(t *testing.T) {
	t.Parallel()
	// Path fields should be preserved, but secrets in content should be caught
	input := `{"file_path":"/tmp/test.go","content":"api_key=` + highEntropySecret + `"}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// file_path should be preserved
	if !strings.Contains(result, "/tmp/test.go") {
		t.Error("file_path was incorrectly modified")
	}

	// Secret in content should be redacted
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in content field was not redacted")
	}
	if !strings.Contains(result, wantRedacted) {
		t.Error("expected REDACTED in output")
	}
}

// Pins a known gap: shell shorthand `--password=...` is not redacted because
// no detector matches `--password=` (no DB-prefix, no DSN structure, no URI).
func TestString_MysqlShellShorthandIsNotRedacted(t *testing.T) {
	t.Parallel()
	assertStringRedactionCases(t, []stringRedactionCase{
		{
			name:  "mysql cli flag",
			input: "mysql -u svc --password=hunter2 -h db.example.com app",
			want:  "mysql -u svc --password=hunter2 -h db.example.com app",
		},
		{
			name:  "psql cli flag",
			input: "psql --password=hunter2 -U svc -h db.example.com app",
			want:  "psql --password=hunter2 -U svc -h db.example.com app",
		},
	})
}

// Pins f(f(x)) == f(x): once-redacted output must not match any detector on
// a second pass.
func TestString_RedactionIsIdempotent(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"DATABASE_URL=postgres://svc:hunter2@db.example.com/app",
		"DB_PASSWORD=hunter2",
		`conn=Server=db.example.com;User ID=svc;Password="se;cret;here";Encrypt=true`,
		"jdbc:postgresql://db.example.com:5432/app?user=svc&password=hunter2",
		"my key is " + highEntropySecret + " ok",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			once := String(input)
			twice := String(once)
			if once != twice {
				t.Errorf("not idempotent for %q:\n  once:  %q\n  twice: %q", input, once, twice)
			}
		})
	}
}

// Pins keyed-JSON replacement as (key, value) rather than (path, value): a
// shared value under the same key name redacts in every context, not just
// the credential one. Conservative on purpose — flag if changed.
func TestJSONLContent_CrossContextValueCollision(t *testing.T) {
	t.Parallel()
	input := `{"db":{"host":"db.example.com","user":"svc","password":"shared-secret"},"misc":{"password":"shared-secret"}}`

	result, err := JSONLContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "shared-secret") {
		t.Errorf("expected shared-secret to be redacted in both contexts, got: %s", result)
	}
	if strings.Count(result, `"password":"`+wantRedacted+`"`) != 2 {
		t.Errorf("expected both password fields redacted, got: %s", result)
	}
}
