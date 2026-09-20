package main

import (
	"errors"
	"strconv"
	"strings"
)

const maxCommitSearchScan = 1000

type commitSearchResult struct {
	Commit    string `json:"commit"`
	Subject   string `json:"subject"`
	Author    string `json:"author"`
	Timestamp int64  `json:"timestamp"`
}

// searchCommits scans a bounded slice of the selected branch's history. It
// matches literal text in the commit ID, author, and complete message, while
// returning only a short subject. Commit bodies may contain sensitive data, so
// the search result does not echo them into the API or browser.
func (s *store) searchCommits(repo, ref, query string) ([]commitSearchResult, bool, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return nil, false, err
	}
	if !validBranchName(path, ref) {
		return nil, false, errors.New("invalid branch")
	}
	if _, _, err := gitOutput(path, 100, "rev-parse", "--verify", "refs/heads/"+ref+"^{commit}"); err != nil {
		return nil, false, errSearchBranchNotFound
	}
	raw, outputTruncated, err := gitOutput(path, 2<<20, "log", "-z", "--max-count=1001", "--format=%H%x00%at%x00%an%x00%B%x00", "refs/heads/"+ref)
	if err != nil {
		return nil, false, err
	}
	records := strings.Split(raw, "\x00\x00")
	results := make([]commitSearchResult, 0)
	truncated := outputTruncated
	needle := strings.ToLower(query)
	scanned := 0
	for _, record := range records {
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x00", 4)
		if len(fields) != 4 || len(fields[0]) != 40 || !isFullHexSHA(fields[0]) {
			truncated = true // The output cap may have cut a record in half.
			continue
		}
		scanned++
		if scanned > maxCommitSearchScan {
			truncated = true
			break
		}
		if !containsSearchTerm(needle, fields[0], fields[2], fields[3]) {
			continue
		}
		if len(results) >= maxSearchResults {
			truncated = true
			break
		}
		timestamp, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		subject := strings.SplitN(strings.TrimSpace(fields[3]), "\n", 2)[0]
		results = append(results, commitSearchResult{Commit: fields[0], Subject: agentSearchExcerpt(subject), Author: agentSearchExcerpt(fields[2]), Timestamp: timestamp})
	}
	return results, truncated, nil
}

func isFullHexSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func recentCommitLinks(path, ref string) []commitSearchResult {
	raw, _, err := gitOutput(path, 32<<10, "log", "-z", "--max-count=5", "--format=%H%x00%at%x00%an%x00%s%x00", "refs/heads/"+ref)
	if err != nil {
		return nil
	}
	results := make([]commitSearchResult, 0, 5)
	for _, record := range strings.Split(raw, "\x00\x00") {
		fields := strings.SplitN(record, "\x00", 4)
		if len(fields) != 4 || !isFullHexSHA(fields[0]) {
			continue
		}
		timestamp, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		results = append(results, commitSearchResult{Commit: fields[0], Timestamp: timestamp, Author: agentSearchExcerpt(fields[2]), Subject: agentSearchExcerpt(fields[3])})
	}
	return results
}
