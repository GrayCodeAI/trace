package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxIndexedFile  = 2 << 20
	maxIndexedBytes = 64 << 20
)

type indexedDocument struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type searchIndex struct {
	Repo   string            `json:"repo"`
	Ref    string            `json:"ref"`
	Commit string            `json:"commit"`
	Docs   []indexedDocument `json:"docs"`
}

func searchIndexFile(root, repo, ref string) string {
	hash := sha256.Sum256([]byte(repo + "\x00" + ref))
	return filepath.Join(root, "search-index", hex.EncodeToString(hash[:])+".json")
}

func (s *store) rebuildSearchIndex(repo, ref string) (searchIndex, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return searchIndex{}, err
	}
	if !validBranchName(path, ref) {
		return searchIndex{}, errors.New("invalid branch")
	}
	commit, err := gitActionOutput(path, "rev-parse", "refs/heads/"+ref)
	if err != nil {
		return searchIndex{}, errors.New("branch not found")
	}
	list, err := exec.Command("git", "--git-dir", path, "ls-tree", "-r", "--name-only", ref).Output()
	if err != nil {
		return searchIndex{}, fmt.Errorf("list files: %w", err)
	}
	index := searchIndex{Repo: repo, Ref: ref, Commit: commit, Docs: make([]indexedDocument, 0)}
	var total int
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		file := strings.TrimSpace(line)
		if file == "" || validateRawPath(file) != nil {
			continue
		}
		content, err := exec.Command("git", "--git-dir", path, "cat-file", "blob", ref+":"+file).Output()
		if err != nil || len(content) == 0 || len(content) > maxIndexedFile || !utf8.Valid(content) || strings.ContainsRune(string(content), '\x00') {
			continue
		}
		if total+len(content) > maxIndexedBytes {
			break
		}
		index.Docs = append(index.Docs, indexedDocument{Path: file, Content: string(content)})
		total += len(content)
	}
	sort.Slice(index.Docs, func(i, j int) bool { return index.Docs[i].Path < index.Docs[j].Path })
	b, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return searchIndex{}, err
	}
	indexPath := searchIndexFile(s.root, repo, ref)
	if err := os.MkdirAll(filepath.Dir(indexPath), 0700); err != nil {
		return searchIndex{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(indexPath), ".search-index-")
	if err != nil {
		return searchIndex{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return searchIndex{}, err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return searchIndex{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return searchIndex{}, err
	}
	if err := tmp.Close(); err != nil {
		return searchIndex{}, err
	}
	if err := os.Rename(name, indexPath); err != nil {
		return searchIndex{}, err
	}
	return index, nil
}

func (s *store) loadSearchIndex(repo, ref string) (searchIndex, bool) {
	b, err := os.ReadFile(searchIndexFile(s.root, repo, ref))
	if err != nil {
		return searchIndex{}, false
	}
	var index searchIndex
	if json.Unmarshal(b, &index) != nil || index.Repo != repo || index.Ref != ref {
		return searchIndex{}, false
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return searchIndex{}, false
	}
	commit, err := gitActionOutput(path, "rev-parse", "refs/heads/"+ref)
	if err != nil || commit != index.Commit {
		return searchIndex{}, false
	}
	return index, true
}

func searchIndexed(index searchIndex, query string) searchResponse {
	response := searchResponse{Query: query, Ref: index.Ref, Results: make([]searchResult, 0)}
	for _, doc := range index.Docs {
		for lineNumber, line := range strings.Split(doc.Content, "\n") {
			if strings.Contains(line, query) {
				response.Results = append(response.Results, searchResult{Path: doc.Path, Line: lineNumber + 1, Content: line})
				if len(response.Results) >= maxSearchResults {
					response.Truncated = true
					return response
				}
			}
		}
	}
	return response
}

func (s *store) search(repo, ref, query string) (searchResponse, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return searchResponse{}, err
	}
	if index, ok := s.loadSearchIndex(repo, ref); ok {
		return searchIndexed(index, strings.TrimSpace(query)), nil
	}
	return searchRepo(path, ref, query)
}

func (a *app) apiSearchIndex(w http.ResponseWriter, r *http.Request, u userRecord, repo string) {
	if r.Method != http.MethodPost || !u.canWrite(repo) {
		apiError(w, http.StatusForbidden, "write access required")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	index, err := a.store.rebuildSearchIndex(repo, ref)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"repo": index.Repo, "ref": index.Ref, "commit": index.Commit, "documents": len(index.Docs)})
}
