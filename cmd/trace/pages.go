package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type pagesConfig struct {
	Enabled bool
	Branch  string
	Root    string
}

func loadPagesConfig(repoPath string) pagesConfig {
	branch, branchErr := exec.Command("git", "-C", repoPath, "config", "--get", "trace.pages.branch").Output()
	root, rootErr := exec.Command("git", "-C", repoPath, "config", "--get", "trace.pages.root").Output()
	if branchErr != nil || rootErr != nil || strings.TrimSpace(string(branch)) == "" {
		return pagesConfig{}
	}
	return pagesConfig{Enabled: true, Branch: strings.TrimSpace(string(branch)), Root: strings.Trim(strings.TrimSpace(string(root)), "/")}
}

func (s *store) setPages(repo, branch, root string, enabled bool) error {
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("repository not found")
	}
	if !enabled {
		for _, key := range []string{"trace.pages.branch", "trace.pages.root"} {
			if out, err := exec.Command("git", "-C", path, "config", "--unset", key).CombinedOutput(); err != nil && !strings.Contains(string(out), "No such section") && !strings.Contains(string(out), "unset failed") {
				return fmt.Errorf("disable pages: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	if !namePattern.MatchString(branch) || branch == "HEAD" {
		return errors.New("pages branch must be a valid branch name")
	}
	root = strings.Trim(strings.TrimSpace(root), "/")
	if root != "" {
		if err := validatePagesPath(root); err != nil {
			return err
		}
	}
	if _, err := exec.Command("git", "-C", path, "rev-parse", "--verify", "refs/heads/"+branch).Output(); err != nil {
		return errors.New("pages branch not found")
	}
	for key, value := range map[string]string{"trace.pages.branch": branch, "trace.pages.root": root} {
		if out, err := exec.Command("git", "-C", path, "config", key, value).CombinedOutput(); err != nil {
			return fmt.Errorf("configure pages: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func validatePagesPath(value string) error {
	clean := path.Clean("/" + value)
	if clean == "/" || clean != "/"+strings.Trim(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "\x00") {
		return errors.New("pages root must be a relative repository path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("pages root contains an unsafe path component")
		}
	}
	return nil
}

func (s *store) pagesFile(repo, requestPath string) ([]byte, string, error) {
	repoPath, err := s.repoPath(repo)
	if err != nil {
		return nil, "", err
	}
	config := loadPagesConfig(repoPath)
	if !config.Enabled {
		return nil, "", os.ErrNotExist
	}
	decoded, err := url.PathUnescape(requestPath)
	if err != nil {
		return nil, "", errors.New("invalid pages path")
	}
	decoded = strings.TrimPrefix(decoded, "/")
	if decoded == "" || strings.HasSuffix(decoded, "/") {
		decoded += "index.html"
	}
	if err := validatePagesPath(decoded); err != nil {
		return nil, "", err
	}
	filePath := decoded
	if config.Root != "" {
		filePath = config.Root + "/" + filePath
	}
	if err := validatePagesPath(filePath); err != nil {
		return nil, "", err
	}
	ref := config.Branch + ":" + filePath
	cmd := exec.Command("git", "--git-dir", repoPath, "show", ref)
	b, err := cmd.Output()
	if err != nil {
		return nil, "", os.ErrNotExist
	}
	if len(b) > 8<<20 {
		return nil, "", errors.New("pages file exceeds 8 MiB limit")
	}
	return b, mime.TypeByExtension(filepath.Ext(filePath)), nil
}

func (a *app) pagesHTTP(w http.ResponseWriter, r *http.Request, db userDB) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/pages/")
	parts := strings.SplitN(tail, "/", 3)
	if len(parts) < 2 || !namePattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	repoPath, err := a.store.repoPath(repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	config := loadPagesConfig(repoPath)
	if !config.Enabled || isArchived(repoPath) {
		http.NotFound(w, r)
		return
	}
	public := isPublic(repoPath)
	if !public {
		username, u, ok := a.webIdentity(r, db)
		_ = username
		if !ok || !u.canRead(repo) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	requestPath := ""
	if len(parts) == 3 {
		requestPath = parts[2]
	}
	b, contentType, err := a.store.pagesFile(repo, requestPath)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = io.Copy(w, bytes.NewReader(b))
}

func (a *app) apiPages(w http.ResponseWriter, r *http.Request, u userRecord, repo string) {
	repoPath, err := a.store.repoPath(repo)
	if err != nil {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method == http.MethodGet {
		config := loadPagesConfig(repoPath)
		writeJSON(w, http.StatusOK, map[string]any{"enabled": config.Enabled, "branch": config.Branch, "root": config.Root, "url": "/pages/" + repo + "/"})
		return
	}
	if !u.Admin && roleFor(u, repo) != "write" {
		apiError(w, http.StatusForbidden, "write access required")
		return
	}
	var input struct {
		Enabled *bool  `json:"enabled"`
		Branch  string `json:"branch"`
		Root    string `json:"root"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if err := a.store.setPages(repo, input.Branch, input.Root, enabled); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	config := loadPagesConfig(repoPath)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": config.Enabled, "branch": config.Branch, "root": config.Root, "url": "/pages/" + repo + "/"})
}
