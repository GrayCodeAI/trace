package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	maxSearchQuery   = 200
	maxSearchResults = 100
	maxSearchOutput  = 512 << 10
)

var errSearchBranchNotFound = errors.New("branch not found")

type searchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type searchResponse struct {
	Query             string               `json:"query"`
	Ref               string               `json:"ref"`
	Scope             string               `json:"scope"`
	Results           []searchResult       `json:"results"`
	Commits           []commitSearchResult `json:"commits"`
	Sessions          []agentSearchResult  `json:"sessions"`
	Truncated         bool                 `json:"truncated"`
	CommitsTruncated  bool                 `json:"commits_truncated"`
	SessionsTruncated bool                 `json:"sessions_truncated"`
}

type agentSearchResult struct {
	SessionID    int    `json:"session_id"`
	CheckpointID int    `json:"checkpoint_id,omitempty"`
	Agent        string `json:"agent"`
	Commit       string `json:"commit"`
	Source       string `json:"source"`
	Excerpt      string `json:"excerpt"`
}

func (s *store) searchContext(repo, ref, query, scope string) (searchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQuery {
		return searchResponse{}, errors.New("search query must be between 1 and 200 characters")
	}
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "code" && scope != "commits" && scope != "sessions" {
		return searchResponse{}, errors.New("scope must be all, code, commits, or sessions")
	}
	if ref == "" {
		ref = "main"
	}
	response := searchResponse{Query: query, Ref: ref, Scope: scope, Results: []searchResult{}, Commits: []commitSearchResult{}, Sessions: []agentSearchResult{}}
	if scope == "all" || scope == "code" {
		code, err := s.search(repo, ref, query)
		if err != nil {
			return searchResponse{}, err
		}
		response.Results, response.Truncated = code.Results, code.Truncated
	}
	if scope == "all" || scope == "commits" {
		commits, truncated, err := s.searchCommits(repo, ref, query)
		if err != nil {
			return searchResponse{}, err
		}
		response.Commits, response.CommitsTruncated = commits, truncated
	}
	if scope == "all" || scope == "sessions" {
		sessions, truncated, err := s.searchAgentSessions(repo, query)
		if err != nil {
			return searchResponse{}, err
		}
		response.Sessions, response.SessionsTruncated = sessions, truncated
	}
	return response, nil
}

func (s *store) searchAgentSessions(repo, query string) ([]agentSearchResult, bool, error) {
	sessions, err := s.listAgentSessions(repo)
	if err != nil {
		return nil, false, err
	}
	results, truncated := searchAgentSessionRecords(sessions, query)
	return results, truncated, nil
}

func searchAgentSessionRecords(sessions []agentSession, query string) ([]agentSearchResult, bool) {
	needle := strings.ToLower(query)
	results := make([]agentSearchResult, 0)
	for _, session := range sessions {
		if containsSearchTerm(needle, session.Agent, session.Summary, session.Commit, session.CreatedBy) {
			results = append(results, agentSearchResult{SessionID: session.ID, Agent: session.Agent, Commit: session.Commit, Source: "session", Excerpt: agentSearchExcerpt(session.Summary)})
			if len(results) > maxSearchResults {
				return results[:maxSearchResults], true
			}
		}
		for _, checkpoint := range session.Checkpoints {
			if !containsSearchTerm(needle, checkpoint.Summary, checkpoint.State, checkpoint.Commit) {
				continue
			}
			results = append(results, agentSearchResult{SessionID: session.ID, CheckpointID: checkpoint.ID, Agent: session.Agent, Commit: checkpoint.Commit, Source: "checkpoint", Excerpt: agentSearchExcerpt(checkpoint.Summary)})
			if len(results) > maxSearchResults {
				return results[:maxSearchResults], true
			}
		}
	}
	return results, false
}

func containsSearchTerm(needle string, fields ...string) bool {
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func agentSearchExcerpt(value string) string {
	runes := []rune(value)
	if len(runes) > 280 {
		return string(runes[:280]) + "…"
	}
	return value
}

func searchRepo(repoPath, ref, query string) (searchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQuery {
		return searchResponse{}, errors.New("search query must be between 1 and 200 characters")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "main"
	}
	if !validBranchName(repoPath, ref) {
		return searchResponse{}, errors.New("invalid branch")
	}
	if _, err := os.Stat(repoPath); err != nil {
		return searchResponse{}, errors.New("repository not found")
	}
	resolve := exec.Command("git", "--git-dir", repoPath, "rev-parse", "--verify", "refs/heads/"+ref)
	if err := resolve.Run(); err != nil {
		return searchResponse{}, errSearchBranchNotFound
	}
	cmd := exec.Command("git", "--git-dir", repoPath, "grep", "-n", "-I", "-F", "--max-count="+strconv.Itoa(maxSearchResults), "-e", query, "refs/heads/"+ref, "--")
	var output cappedBuffer
	output.limit = maxSearchOutput
	cmd.Stdout = &output
	cmd.Stderr = &cappedBuffer{limit: 4096}
	err := cmd.Run()
	// git grep returns 1 when there are no matches.
	if err != nil && !isGitNoMatch(err) {
		return searchResponse{}, fmt.Errorf("search: %w", err)
	}
	response := searchResponse{Query: query, Ref: ref, Results: make([]searchResult, 0), Truncated: output.truncated}
	for _, raw := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		fields := strings.SplitN(raw, ":", 4)
		if len(fields) == 4 && fields[0] == "refs/heads/"+ref {
			fields = fields[1:]
		}
		if len(fields) != 3 {
			continue
		}
		line, err := strconv.Atoi(fields[1])
		if err != nil || line < 1 {
			continue
		}
		response.Results = append(response.Results, searchResult{Path: fields[0], Line: line, Content: fields[2]})
		if len(response.Results) >= maxSearchResults {
			response.Truncated = true
			break
		}
	}
	return response, nil
}

func isGitNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func (a *app) apiSearch(w http.ResponseWriter, r *http.Request, repo, repoPath string) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := a.store.searchContext(repo, r.URL.Query().Get("ref"), r.URL.Query().Get("q"), r.URL.Query().Get("scope"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
