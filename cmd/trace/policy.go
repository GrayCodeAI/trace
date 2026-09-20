package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const policyFile = "policies.json"

type repoPolicy struct {
	RequiredApprovals int      `json:"required_approvals"`
	RequiredChecks    []string `json:"required_checks,omitempty"`
	ProtectedBranches []string `json:"protected_branches,omitempty"`
	RequireCodeOwners bool     `json:"require_codeowners,omitempty"`
}

type policyDB struct {
	Repos map[string]repoPolicy `json:"repos"`
}

func defaultRepoPolicy() repoPolicy {
	return repoPolicy{RequiredApprovals: 1, RequiredChecks: []string{}, ProtectedBranches: []string{"main"}}
}

func (s *store) loadPolicies() (policyDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, policyFile))
	if errors.Is(err, os.ErrNotExist) {
		return policyDB{Repos: map[string]repoPolicy{}}, nil
	}
	if err != nil {
		return policyDB{}, err
	}
	var db policyDB
	if err := json.Unmarshal(b, &db); err != nil {
		return policyDB{}, fmt.Errorf("read policies: %w", err)
	}
	if db.Repos == nil {
		db.Repos = map[string]repoPolicy{}
	}
	return db, nil
}

func (s *store) updatePolicies(change func(*policyDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".policies.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadPolicies()
	if err != nil {
		return err
	}
	if err := change(&db); err != nil {
		return err
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".policies-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.root, policyFile))
}

func normalizePolicy(policy repoPolicy) (repoPolicy, error) {
	if policy.RequiredApprovals < 0 || policy.RequiredApprovals > 100 {
		return repoPolicy{}, errors.New("required_approvals must be between 0 and 100")
	}
	if len(policy.RequiredChecks) > 100 {
		return repoPolicy{}, errors.New("too many required checks")
	}
	seen := map[string]bool{}
	checks := make([]string, 0, len(policy.RequiredChecks))
	for _, check := range policy.RequiredChecks {
		check = strings.TrimSpace(check)
		if check == "" || len(check) > 100 {
			return repoPolicy{}, errors.New("required check names must be between 1 and 100 characters")
		}
		if !seen[check] {
			seen[check] = true
			checks = append(checks, check)
		}
	}
	sort.Strings(checks)
	policy.RequiredChecks = checks
	if len(policy.ProtectedBranches) == 0 {
		policy.ProtectedBranches = []string{"main"}
	}
	seen = map[string]bool{}
	branches := make([]string, 0, len(policy.ProtectedBranches))
	for _, branch := range policy.ProtectedBranches {
		branch = strings.TrimSpace(branch)
		if branch == "" || len(branch) > 100 || strings.HasPrefix(branch, "refs/") || strings.ContainsAny(branch, " ~^:?[]\\") {
			return repoPolicy{}, errors.New("protected branch patterns must be valid branch names and may use * as a wildcard")
		}
		if strings.Contains(branch, "*") && branch != "*" && !strings.HasSuffix(branch, "*") {
			return repoPolicy{}, errors.New("protected branch wildcards must appear at the end of a pattern")
		}
		if !seen[branch] {
			seen[branch] = true
			branches = append(branches, branch)
		}
	}
	sort.Strings(branches)
	policy.ProtectedBranches = branches
	return policy, nil
}

func (s *store) repoPolicy(repo string) (repoPolicy, error) {
	if _, err := s.repoPath(repo); err != nil {
		return repoPolicy{}, err
	}
	db, err := s.loadPolicies()
	if err != nil {
		return repoPolicy{}, err
	}
	policy := defaultRepoPolicy()
	if stored, ok := db.Repos[repo]; ok {
		policy = stored
	}
	return normalizePolicy(policy)
}

func (s *store) setRepoPolicy(repo string, policy repoPolicy) (repoPolicy, error) {
	if _, err := s.repoPath(repo); err != nil {
		return repoPolicy{}, err
	}
	policy, err := normalizePolicy(policy)
	if err != nil {
		return repoPolicy{}, err
	}
	err = s.updatePolicies(func(db *policyDB) error {
		db.Repos[repo] = policy
		return nil
	})
	if err == nil {
		path, pathErr := s.repoPath(repo)
		if pathErr != nil {
			return repoPolicy{}, pathErr
		}
		err = installHookWithPolicy(path, policy)
	}
	return policy, err
}

func (s *store) requiredChecksPass(repo, commit string, checks []string) bool {
	if len(checks) == 0 {
		return true
	}
	runs, err := s.listActionRuns(repo)
	if err != nil {
		return false
	}
	passed := map[string]bool{}
	for _, run := range runs {
		if run.Commit != commit || run.Status != "success" {
			continue
		}
		for _, job := range run.Jobs {
			if job.Status == "success" {
				passed[job.Name] = true
			}
		}
	}
	for _, check := range checks {
		if !passed[check] {
			return false
		}
	}
	return true
}
