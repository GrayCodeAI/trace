package main

import (
	_ "embed"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed commit.html
var commitHTML string

var commitTemplate = template.Must(template.New("commit").Parse(strings.Replace(commitHTML, "<!-- TRACE_COMMIT_STYLES -->", pageStyle+repoPageStyle, 1)))

type commitPageData struct {
	Name          string
	Ref           string
	Commit        string
	Subject       string
	Message       string
	Author        string
	Email         string
	Date          string
	Diff          string
	DiffTruncated bool
}

func (a *app) commitPage(w http.ResponseWriter, r *http.Request, u userRecord) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) != 4 || parts[2] != "commits" || !isFullHexSHA(parts[3]) {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if !validRepoName(repo) || !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	path, err := a.store.repoPath(repo)
	if err != nil || !validBranchName(path, ref) {
		http.NotFound(w, r)
		return
	}
	commit := strings.ToLower(parts[3])
	if _, _, err := gitOutput(path, 100, "rev-parse", "--verify", "refs/heads/"+ref+"^{commit}"); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, _, err := gitOutput(path, 100, "merge-base", "--is-ancestor", commit, "refs/heads/"+ref); err != nil {
		http.NotFound(w, r)
		return
	}
	raw, messageTruncated, err := gitOutput(path, 64<<10, "show", "-s", "--format=%H%x00%an%x00%ae%x00%aI%x00%B", commit)
	if err != nil {
		http.Error(w, "cannot read commit", http.StatusInternalServerError)
		return
	}
	fields := strings.SplitN(raw, "\x00", 5)
	if len(fields) != 5 || !strings.EqualFold(fields[0], commit) {
		http.Error(w, "cannot parse commit", http.StatusInternalServerError)
		return
	}
	diff, diffTruncated, err := gitOutput(path, 512<<10, "show", "--format=", "--first-parent", "--no-ext-diff", "--no-color", "--no-renames", commit, "--")
	if err != nil {
		http.Error(w, "cannot read commit diff", http.StatusInternalServerError)
		return
	}
	message := strings.TrimSpace(fields[4])
	if messageTruncated {
		message += "\n\n[Commit message preview truncated.]"
	}
	date := fields[3]
	if parsed, err := time.Parse(time.RFC3339, date); err == nil {
		date = parsed.Format("2 Jan 2006, 15:04 MST")
	}
	data := commitPageData{Name: repo, Ref: ref, Commit: commit, Subject: strings.SplitN(message, "\n", 2)[0], Message: message, Author: fields[1], Email: fields[2], Date: date, Diff: diff, DiffTruncated: diffTruncated}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := commitTemplate.Execute(w, data); err != nil {
		log.Printf("render commit: %v", err)
	}
}
