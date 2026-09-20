package main

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxRawFile = 8 << 20

func validateRawPath(value string) error {
	value, err := url.PathUnescape(value)
	if err != nil || value == "" || strings.Contains(value, "\x00") || strings.Contains(value, "\\") {
		return errors.New("invalid raw file path")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean != value || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return errors.New("invalid raw file path")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid raw file path")
		}
	}
	return nil
}

func (s *store) rawFile(repo, ref, file string) ([]byte, string, error) {
	repoPath, err := s.repoPath(repo)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(ref) == "" || !validBranchName(repoPath, ref) {
		return nil, "", errors.New("invalid branch")
	}
	if err := validateRawPath(file); err != nil {
		return nil, "", err
	}
	cmd := exec.Command("git", "--git-dir", repoPath, "cat-file", "blob", ref+":"+file)
	b, err := cmd.Output()
	if err != nil {
		return nil, "", os.ErrNotExist
	}
	if len(b) > maxRawFile {
		return nil, "", errors.New("raw file exceeds 8 MiB limit")
	}
	return b, mime.TypeByExtension(filepath.Ext(file)), nil
}

func writeRaw(w http.ResponseWriter, body []byte, contentType string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", stringSize(len(body)))
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = io.Copy(w, strings.NewReader(string(body)))
}

func stringSize(size int) string {
	// Avoid strconv in the hot path's callers while keeping Content-Length exact.
	if size == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for size > 0 {
		i--
		digits[i] = byte('0' + size%10)
		size /= 10
	}
	return string(digits[i:])
}

func (a *app) rawHTTP(w http.ResponseWriter, r *http.Request, db userDB) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/raw/")
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
	public := isPublic(repoPath)
	if !public {
		username, u, ok := a.webIdentity(r, db)
		_ = username
		if !ok || !u.canRead(repo) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	body, contentType, err := a.store.rawFile(repo, ref, parts[2])
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeRaw(w, body, contentType)
}

func (a *app) apiRaw(w http.ResponseWriter, r *http.Request, repo, repoPath string) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	file := r.URL.Query().Get("path")
	body, contentType, err := a.store.rawFile(repo, ref, file)
	if errors.Is(err, os.ErrNotExist) {
		apiError(w, http.StatusNotFound, "raw file not found")
		return
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeRaw(w, body, contentType)
	_ = repoPath
}
