package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	renderedScriptPattern  = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	inlineHandlerPattern   = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	scriptSrcDirectivePart = regexp.MustCompile(`script-src ([^;]*)`)
)

// TestAppPagesNeedNoUnsafeInlineScripts renders Trace's own pages and checks
// that every inline script is allowed by hash, that script-src does not
// contain 'unsafe-inline', and that no inline event handlers remain.
func TestAppPagesNeedNoUnsafeInlineScripts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/app", false); err != nil {
		t.Fatal(err)
	}
	commitFiles(t, a.store, "team/app", map[string][]byte{"README.md": []byte("hello\n")})
	repoPath, _ := a.store.repoPath("team/app")
	head, err := gitActionOutput(repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	paths := []string{"/", "/login", "/app", "/search?q=hello", "/settings/federation", "/settings/teams", "/repos/team/app", "/repos/team/app/actions", "/repos/team/app/agents", "/repos/team/app/issues", "/repos/team/app/releases", "/repos/team/app/packages", "/repos/team/app/projects", "/repos/team/app/settings/policy", "/repos/team/app/search?q=hello", "/repos/team/app/commits/" + head}
	rendered := 0
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if path != "/" && path != "/login" {
			req.SetBasicAuth("admin", token)
		}
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != http.StatusOK || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s: %d %s", path, res.Code, res.Header().Get("Content-Type"))
		}
		rendered++
		csp := res.Header().Get("Content-Security-Policy")
		scriptSrc := scriptSrcDirectivePart.FindStringSubmatch(csp)
		if scriptSrc == nil || strings.Contains(scriptSrc[1], "unsafe-inline") {
			t.Fatalf("%s: script-src must not allow unsafe-inline: %q", path, csp)
		}
		body := res.Body.String()
		for _, match := range renderedScriptPattern.FindAllStringSubmatch(body, -1) {
			if strings.TrimSpace(match[1]) != "" {
				t.Fatalf("%s: unexpected script attributes %q", path, match[1])
			}
			sum := sha256.Sum256([]byte(match[2]))
			if hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !strings.Contains(scriptSrc[1], hash) {
				t.Fatalf("%s: inline script is not covered by the CSP (%s)", path, hash)
			}
		}
		if handler := inlineHandlerPattern.FindString(body); handler != "" {
			t.Fatalf("%s: inline event handler %q would be blocked by the CSP", path, handler)
		}
	}
	if rendered != len(paths) {
		t.Fatalf("rendered %d of %d pages", rendered, len(paths))
	}
}

// TestRawAndPagesCannotRunScriptsAsTheUser covers the stored-XSS path: a
// writer commits HTML/SVG with script, and an administrator opens it through
// /raw or /pages on Trace's origin.
func TestRawAndPagesCannotRunScriptsAsTheUser(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/site", false); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`<script>window.open('/app')</script>`)
	commitFiles(t, a.store, "team/site", map[string][]byte{"index.html": payload, "evil.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "evil.js": []byte("alert(1)"), "logo.png": []byte("\x89PNG\r\n\x1a\n")})
	if err := a.store.setPages("team/site", "main", "", true); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("admin", token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, res.Code)
		}
		return res
	}
	for _, path := range []string{"/raw/team/site/index.html", "/raw/team/site/evil.svg", "/raw/team/site/evil.js", "/api/v1/repos/team/site/raw?path=index.html"} {
		res := get(path)
		if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("%s: active content served as %q", path, ct)
		}
		if csp := res.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-scripts") {
			t.Fatalf("%s: raw content is not sandboxed: %q", path, csp)
		}
		if res.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: missing nosniff", path)
		}
	}
	if ct := get("/raw/team/site/logo.png").Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("passive image type changed: %q", ct)
	}
	page := get("/pages/team/site/")
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-top-navigation") {
		t.Fatalf("Pages are not isolated in an opaque-origin sandbox: %q", csp)
	}
	if !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") || page.Body.String() != string(payload) {
		t.Fatalf("Pages site content changed: %q %q", page.Header().Get("Content-Type"), page.Body.String())
	}
}
