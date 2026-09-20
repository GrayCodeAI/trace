package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

type codeOwnerRule struct {
	Pattern string
	Owners  []string
}

var codeOwnerFiles = []string{"CODEOWNERS", ".github/CODEOWNERS", ".gitlab/CODEOWNERS", "docs/CODEOWNERS"}

func parseCodeOwners(raw string) []codeOwnerRule {
	var rules []codeOwnerRule
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		owners := make([]string, 0, len(fields)-1)
		for _, owner := range fields[1:] {
			owner = strings.TrimPrefix(strings.TrimSpace(owner), "@")
			if owner != "" && owner != "*" {
				owners = append(owners, owner)
			}
		}
		if len(owners) > 0 {
			rules = append(rules, codeOwnerRule{Pattern: fields[0], Owners: owners})
		}
	}
	return rules
}

func codeOwnerMatches(pattern, filename string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	filename = strings.TrimPrefix(filename, "./")
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(filename, strings.TrimSuffix(pattern, "/"))
	}
	if strings.Contains(pattern, "/") {
		return recursiveGlobMatch(strings.Split(pattern, "/"), strings.Split(filename, "/"))
	}
	matched, _ := path.Match(pattern, filename)
	if matched {
		return true
	}
	matched, _ = path.Match(pattern, path.Base(filename))
	return matched
}

// recursiveGlobMatch implements the CODEOWNERS path subset that ordinary
// path.Match cannot express: ** consumes zero or more complete path segments.
func recursiveGlobMatch(pattern, filename []string) bool {
	type key struct{ p, f int }
	memo := map[key]bool{}
	seen := map[key]bool{}
	var match func(int, int) bool
	match = func(pi, fi int) bool {
		k := key{pi, fi}
		if seen[k] {
			return memo[k]
		}
		seen[k] = true
		var result bool
		switch {
		case pi == len(pattern):
			result = fi == len(filename)
		case pattern[pi] == "**":
			result = match(pi+1, fi) || (fi < len(filename) && match(pi, fi+1))
		case fi < len(filename):
			segmentMatch, _ := path.Match(pattern[pi], filename[fi])
			result = segmentMatch && match(pi+1, fi+1)
		}
		memo[k] = result
		return result
	}
	return match(0, 0)
}

func (s *store) codeOwnerRules(repo, commit string) ([]codeOwnerRule, error) {
	repoPath, err := s.repoPath(repo)
	if err != nil {
		return nil, err
	}
	for _, candidate := range codeOwnerFiles {
		raw, _, showErr := gitOutput(repoPath, 1<<20, "show", commit+":"+candidate)
		if showErr == nil {
			return parseCodeOwners(raw), nil
		}
	}
	return nil, nil
}

func (s *store) codeOwnerApprovalsPass(repo string, pr pullRequest) (bool, error) {
	rules, err := s.codeOwnerRules(repo, pr.HeadSHA)
	if err != nil || len(rules) == 0 {
		return true, err
	}
	repoPath, err := s.repoPath(repo)
	if err != nil {
		return false, err
	}
	diff, _, err := gitOutput(repoPath, 1<<20, "diff", "--name-only", pr.BaseSHA, pr.HeadSHA, "--")
	if err != nil {
		return false, err
	}
	teams, err := s.loadTeams()
	if err != nil {
		return false, err
	}
	approved := map[string]bool{}
	for _, approval := range pr.Approvals {
		if approval.User != pr.Author {
			approved[approval.User] = true
		}
	}
	missing := make([]string, 0)
	for _, filename := range strings.Split(strings.TrimSpace(diff), "\n") {
		filename = strings.TrimSpace(filename)
		if filename == "" {
			continue
		}
		var owners []string
		for _, rule := range rules {
			if codeOwnerMatches(rule.Pattern, filename) {
				owners = rule.Owners
			}
		}
		if len(owners) == 0 {
			continue
		}
		covered := false
		for _, owner := range owners {
			if approved[owner] {
				covered = true
				break
			}
			if team, ok := teams.Teams[owner]; ok && team.Members != nil {
				for member := range team.Members {
					if approved[member] {
						covered = true
						break
					}
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			missing = append(missing, filename)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return false, fmt.Errorf("code owner approval required for: %s", strings.Join(missing, ", "))
	}
	return true, nil
}
