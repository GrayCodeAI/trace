package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

func (a *app) pypiHTTP(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/pypi/"), "/"), "/")
	if len(parts) < 2 || !namePattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		if !u.canWrite(repo) || isArchived(mustRepoPath(a.store, repo)) || isMirror(mustRepoPath(a.store, repo)) {
			apiError(w, http.StatusForbidden, "write access required and repository must be writable")
			return
		}
		if err := r.ParseMultipartForm(maxPackageSize + (2 << 20)); err != nil {
			apiError(w, http.StatusBadRequest, "invalid PyPI multipart upload")
			return
		}
		name, version := r.FormValue("name"), r.FormValue("version")
		file, header, err := r.FormFile("content")
		if err != nil || !validPackagePart(name) || !validPackagePart(version) {
			apiError(w, http.StatusBadRequest, "name, version, and content are required")
			return
		}
		defer file.Close()
		artifact, err := a.store.publishPackage(repo, name, version, header.Filename, username, file, header.Size)
		if err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, artifact)
		return
	}
	if len(parts) < 3 || parts[2] != "simple" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	items, err := a.store.listPackages(repo)
	if err != nil {
		http.Error(w, "cannot list packages", http.StatusInternalServerError)
		return
	}
	if len(parts) == 3 {
		seen := map[string]bool{}
		var body strings.Builder
		body.WriteString("<!doctype html><html><body>\n")
		for _, item := range items {
			if !seen[item.Name] {
				seen[item.Name] = true
				fmt.Fprintf(&body, "<a href=\"%s/\">%s</a>\n", html.EscapeString(item.Name), html.EscapeString(item.Name))
			}
		}
		body.WriteString("</body></html>\n")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body.String()))
		return
	}
	if len(parts) == 4 {
		packageName := parts[3]
		var body strings.Builder
		body.WriteString("<!doctype html><html><body>\n")
		for _, item := range items {
			if item.Name != packageName {
				continue
			}
			link := "/packages/" + repo + ".git/" + item.Name + "/" + item.Version + "/" + item.Filename + "#sha256=" + item.SHA256
			fmt.Fprintf(&body, "<a href=\"%s\">%s</a><br>\n", html.EscapeString(link), html.EscapeString(item.Filename))
		}
		body.WriteString("</body></html>\n")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body.String()))
		return
	}
	_ = username
	http.NotFound(w, r)
}
