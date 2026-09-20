package main

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxGlobalSearchRepos = 10

type globalCodeHit struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type globalSessionHit struct {
	Repo         string `json:"repo"`
	SessionID    int    `json:"session_id"`
	CheckpointID int    `json:"checkpoint_id,omitempty"`
	Agent        string `json:"agent"`
	Commit       string `json:"commit"`
	Source       string `json:"source"`
	Excerpt      string `json:"excerpt"`
}

type globalCommitHit struct {
	Repo      string `json:"repo"`
	Commit    string `json:"commit"`
	Subject   string `json:"subject"`
	Author    string `json:"author"`
	Timestamp int64  `json:"timestamp"`
}

type globalSearchResponse struct {
	Query             string             `json:"query"`
	Ref               string             `json:"ref"`
	Scope             string             `json:"scope"`
	ReposScanned      int                `json:"repos_scanned"`
	NextAfter         string             `json:"next_after,omitempty"`
	Results           []globalCodeHit    `json:"results"`
	Commits           []globalCommitHit  `json:"commits"`
	Sessions          []globalSessionHit `json:"sessions"`
	CodeTruncated     bool               `json:"code_truncated"`
	CommitsTruncated  bool               `json:"commits_truncated"`
	SessionsTruncated bool               `json:"sessions_truncated"`
}

func (s *store) readableSearchRepos(u userRecord, after string) ([]string, string, error) {
	if after != "" && !validRepoName(after) {
		return nil, "", errors.New("invalid repository cursor")
	}
	owners, err := os.ReadDir(s.repos)
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, maxGlobalSearchRepos+1)
outer:
	for _, owner := range owners {
		if !owner.IsDir() || !namePattern.MatchString(owner.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.repos, owner.Name()))
		if err != nil {
			return nil, "", err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
				continue
			}
			base := strings.TrimSuffix(entry.Name(), ".git")
			if !namePattern.MatchString(base) {
				continue
			}
			name := owner.Name() + "/" + base
			if name <= after || !u.canRead(name) {
				continue
			}
			names = append(names, name)
			if len(names) > maxGlobalSearchRepos {
				break outer
			}
		}
	}
	if len(names) > maxGlobalSearchRepos {
		return names[:maxGlobalSearchRepos], names[maxGlobalSearchRepos-1], nil
	}
	return names, "", nil
}

func (s *store) searchAll(u userRecord, query, ref, scope, after string) (globalSearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQuery {
		return globalSearchResponse{}, errors.New("search query must be between 1 and 200 characters")
	}
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "code" && scope != "commits" && scope != "sessions" {
		return globalSearchResponse{}, errors.New("scope must be all, code, commits, or sessions")
	}
	if ref == "" {
		ref = "main"
	}
	if scope != "sessions" && (len(ref) > 200 || strings.HasPrefix(ref, "-") || exec.Command("git", "check-ref-format", "--branch", ref).Run() != nil) {
		return globalSearchResponse{}, errors.New("invalid branch")
	}
	names, next, err := s.readableSearchRepos(u, after)
	if err != nil {
		return globalSearchResponse{}, err
	}
	response := globalSearchResponse{Query: query, Ref: ref, Scope: scope, ReposScanned: len(names), NextAfter: next, Results: []globalCodeHit{}, Commits: []globalCommitHit{}, Sessions: []globalSessionHit{}}
	sessionsByRepo := make(map[string][]agentSession)
	if scope == "all" || scope == "sessions" {
		db, err := s.loadAgentSessions()
		if err != nil {
			return globalSearchResponse{}, err
		}
		allowed := make(map[string]bool, len(names))
		for _, name := range names {
			allowed[name] = true
		}
		for i := len(db.Sessions) - 1; i >= 0; i-- {
			session := db.Sessions[i]
			if allowed[session.Repo] {
				sessionsByRepo[session.Repo] = append(sessionsByRepo[session.Repo], session)
			}
		}
	}
	for _, name := range names {
		if scope == "all" || scope == "code" {
			code, err := s.search(name, ref, query)
			if err != nil && !errors.Is(err, errSearchBranchNotFound) {
				return globalSearchResponse{}, err
			}
			if err == nil {
				for _, hit := range code.Results {
					response.Results = append(response.Results, globalCodeHit{Repo: name, Path: hit.Path, Line: hit.Line, Content: hit.Content})
				}
				response.CodeTruncated = response.CodeTruncated || code.Truncated
			}
		}
		if scope == "all" || scope == "commits" {
			commits, truncated, err := s.searchCommits(name, ref, query)
			if err != nil && !errors.Is(err, errSearchBranchNotFound) {
				return globalSearchResponse{}, err
			}
			if err == nil {
				for _, hit := range commits {
					response.Commits = append(response.Commits, globalCommitHit{Repo: name, Commit: hit.Commit, Subject: hit.Subject, Author: hit.Author, Timestamp: hit.Timestamp})
				}
				response.CommitsTruncated = response.CommitsTruncated || truncated
			}
		}
		if scope == "all" || scope == "sessions" {
			hits, truncated := searchAgentSessionRecords(sessionsByRepo[name], query)
			for _, hit := range hits {
				response.Sessions = append(response.Sessions, globalSessionHit{Repo: name, SessionID: hit.SessionID, CheckpointID: hit.CheckpointID, Agent: hit.Agent, Commit: hit.Commit, Source: hit.Source, Excerpt: hit.Excerpt})
			}
			response.SessionsTruncated = response.SessionsTruncated || truncated
		}
	}
	return response, nil
}

func (a *app) apiGlobalSearch(w http.ResponseWriter, r *http.Request, u userRecord) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	result, err := a.store.searchAll(u, query.Get("q"), query.Get("ref"), query.Get("scope"), query.Get("after"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
