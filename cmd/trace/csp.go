package main

import (
	"crypto/sha256"
	"encoding/base64"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Trace serves three kinds of HTML on one origin, each with its own policy:
//
//   - its own pages, whose only scripts are the static inline scripts in the
//     embedded templates; they are allowed by SHA-256 hash, so script-src
//     needs no 'unsafe-inline' and injected markup cannot run script;
//   - raw repository files, which are data: active formats are downgraded to
//     text/plain and every response is sandboxed with no script;
//   - Pages sites, which may run their own scripts but only inside a CSP
//     sandbox without allow-same-origin, so they get an opaque origin and
//     cannot read Trace pages, cookies, or CSRF tokens as the signed-in user.

var inlineScriptPattern = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// inlineScriptHashes returns the CSP hash sources for every inline script in
// the given template sources.
func inlineScriptHashes(sources ...string) []string {
	seen := map[string]bool{}
	var hashes []string
	for _, source := range sources {
		for _, match := range inlineScriptPattern.FindAllStringSubmatch(source, -1) {
			sum := sha256.Sum256([]byte(match[1]))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !seen[hash] {
				seen[hash] = true
				hashes = append(hashes, hash)
			}
		}
	}
	sort.Strings(hashes)
	return hashes
}

var appContentSecurityPolicy = "default-src 'none'; script-src " +
	strings.Join(inlineScriptHashes(pageStyle, dashboardHTML, repoHTML, loginHTML, string(landingHTML), agentHTML, commitHTML), " ") +
	"; style-src 'unsafe-inline'; font-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// rawContentSecurityPolicy renders raw files as inert documents.
const rawContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// pagesContentSecurityPolicy lets a Pages site use its own scripts, styles,
// and assets while the sandbox gives it an opaque origin.
const pagesContentSecurityPolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; default-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' data: blob:; connect-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'"

// rawContentType keeps passive media types and turns anything a browser could
// execute or render as a document (HTML, SVG, XML, JavaScript) into
// text/plain.
func rawContentType(detected string) string {
	if detected == "" {
		return "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(detected)
	if err != nil {
		return "application/octet-stream"
	}
	switch {
	case mediaType == "image/svg+xml":
		return "text/plain; charset=utf-8"
	case strings.HasPrefix(mediaType, "image/"), strings.HasPrefix(mediaType, "audio/"), strings.HasPrefix(mediaType, "video/"), strings.HasPrefix(mediaType, "font/"),
		mediaType == "application/pdf", mediaType == "application/json", mediaType == "application/zip", mediaType == "application/gzip", mediaType == "application/wasm", mediaType == "application/octet-stream":
		return detected
	case strings.HasPrefix(mediaType, "text/"), strings.HasSuffix(mediaType, "+xml"), strings.HasSuffix(mediaType, "/xml"), strings.Contains(mediaType, "javascript"), strings.Contains(mediaType, "ecmascript"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func setRawSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", rawContentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
