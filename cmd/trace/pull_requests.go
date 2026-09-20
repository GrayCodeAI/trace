package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var execCommand = exec.Command

var execCommandOutput = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

const pullRequestFile = "pull-requests.json"

type pullRequest struct {
	ID          int            `json:"id"`
	Repo        string         `json:"repo"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Author      string         `json:"author"`
	Source      string         `json:"source"`
	Target      string         `json:"target"`
	BaseSHA     string         `json:"base_sha"`
	HeadSHA     string         `json:"head_sha"`
	State       string         `json:"state"`
	Draft       bool           `json:"draft,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
	Assignees   []string       `json:"assignees,omitempty"`
	Reviewers   []string       `json:"reviewers,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Comments    []pullComment  `json:"comments,omitempty"`
	Approvals   []pullApproval `json:"approvals,omitempty"`
}

type pullComment struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Path      string    `json:"path,omitempty"`
	Line      int       `json:"line,omitempty"`
	Side      string    `json:"side,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type pullApproval struct {
	User      string    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

type pullRequestDB struct {
	Repos map[string][]pullRequest `json:"repos"`
}

func (s *store) loadPullRequests() (pullRequestDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, pullRequestFile))
	if errors.Is(err, os.ErrNotExist) {
		return pullRequestDB{Repos: make(map[string][]pullRequest)}, nil
	}
	if err != nil {
		return pullRequestDB{}, err
	}
	var db pullRequestDB
	if err := json.Unmarshal(b, &db); err != nil {
		return pullRequestDB{}, fmt.Errorf("read pull requests: %w", err)
	}
	if db.Repos == nil {
		db.Repos = make(map[string][]pullRequest)
	}
	return db, nil
}

func (s *store) updatePullRequests(change func(*pullRequestDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".pull-requests.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadPullRequests()
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
	tmp, err := os.CreateTemp(s.root, ".pull-requests-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
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
	return os.Rename(tmpName, filepath.Join(s.root, pullRequestFile))
}

func validBranchName(repoPath, branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return false
	}
	cmd := execCommand("git", "-C", repoPath, "check-ref-format", "--branch", branch)
	return cmd.Run() == nil
}

func branchSHA(repoPath, branch string) (string, error) {
	if !validBranchName(repoPath, branch) {
		return "", errors.New("invalid branch name")
	}
	out, err := execCommandOutput("git", "-C", repoPath, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", errors.New("branch not found")
	}
	return strings.TrimSpace(out), nil
}

func (s *store) createPullRequest(repo, title, description, author, source, target string) (pullRequest, error) {
	return s.createPullRequestWithDraft(repo, title, description, author, source, target, false)
}

func (s *store) createPullRequestWithDraft(repo, title, description, author, source, target string, draft bool) (pullRequest, error) {
	if strings.TrimSpace(title) == "" || len(title) > 200 {
		return pullRequest{}, errors.New("title must be between 1 and 200 characters")
	}
	if len(description) > 20000 {
		return pullRequest{}, errors.New("description is too long")
	}
	if source == "" {
		return pullRequest{}, errors.New("source branch is required")
	}
	if target == "" {
		target = "main"
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return pullRequest{}, err
	}
	head, err := branchSHA(path, source)
	if err != nil {
		return pullRequest{}, fmt.Errorf("source: %w", err)
	}
	base, err := branchSHA(path, target)
	if err != nil {
		return pullRequest{}, fmt.Errorf("target: %w", err)
	}
	var created pullRequest
	err = s.updatePullRequests(func(db *pullRequestDB) error {
		for _, existing := range db.Repos[repo] {
			if existing.State == "open" && existing.Source == source && existing.Target == target {
				return errors.New("an open pull request already exists for these branches")
			}
		}
		id := 1
		for _, existing := range db.Repos[repo] {
			if existing.ID >= id {
				id = existing.ID + 1
			}
		}
		now := time.Now().UTC()
		created = pullRequest{ID: id, Repo: repo, Title: strings.TrimSpace(title), Description: description, Author: author, Source: source, Target: target, HeadSHA: head, BaseSHA: base, State: "open", Draft: draft, CreatedAt: now, UpdatedAt: now}
		db.Repos[repo] = append(db.Repos[repo], created)
		return nil
	})
	return created, err
}

func normalizePullMetadata(labels, assignees []string) ([]string, []string, error) {
	cleanLabels, err := validateIssueLabels(labels)
	if err != nil {
		return nil, nil, err
	}
	if len(assignees) > 20 {
		return nil, nil, errors.New("at most 20 pull-request assignees are allowed")
	}
	seen := map[string]bool{}
	cleanAssignees := make([]string, 0, len(assignees))
	for _, assignee := range assignees {
		assignee = strings.TrimSpace(assignee)
		if !namePattern.MatchString(assignee) {
			return nil, nil, errors.New("pull-request assignees must be valid Trace usernames")
		}
		if !seen[assignee] {
			seen[assignee] = true
			cleanAssignees = append(cleanAssignees, assignee)
		}
	}
	sort.Strings(cleanAssignees)
	return cleanLabels, cleanAssignees, nil
}

func (s *store) updatePullMetadata(repo string, id int, labels, assignees []string) (pullRequest, error) {
	cleanLabels, cleanAssignees, err := normalizePullMetadata(labels, assignees)
	if err != nil {
		return pullRequest{}, err
	}
	var updated pullRequest
	err = s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		pr.Labels, pr.Assignees = cleanLabels, cleanAssignees
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) updatePullReviewers(repo string, id int, reviewers []string) (pullRequest, error) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(reviewers))
	for _, reviewer := range reviewers {
		reviewer = strings.TrimSpace(reviewer)
		if reviewer == "" {
			continue
		}
		if !namePattern.MatchString(reviewer) {
			return pullRequest{}, errors.New("pull-request reviewers must be valid Trace usernames")
		}
		if !seen[reviewer] {
			seen[reviewer] = true
			clean = append(clean, reviewer)
		}
	}
	sort.Strings(clean)
	if len(clean) > 50 {
		return pullRequest{}, errors.New("at most 50 pull-request reviewers are allowed")
	}
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		pr.Reviewers = clean
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) markPullRequestReady(repo string, id int, user string, admin bool) (pullRequest, error) {
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" || !pr.Draft {
			return errors.New("pull request is already ready or closed")
		}
		if !admin && pr.Author != user {
			return errors.New("only the author or an admin can mark a pull request ready")
		}
		pr.Draft = false
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func findPullRequest(db *pullRequestDB, repo string, id int) (*pullRequest, error) {
	prs := db.Repos[repo]
	for i := range prs {
		if prs[i].ID == id {
			return &prs[i], nil
		}
	}
	return nil, errors.New("pull request not found")
}

func (s *store) getPullRequest(repo string, id int) (pullRequest, error) {
	db, err := s.loadPullRequests()
	if err != nil {
		return pullRequest{}, err
	}
	pr, err := findPullRequest(&db, repo, id)
	if err != nil {
		return pullRequest{}, err
	}
	return *pr, nil
}

func (s *store) addPullComment(repo string, id int, author, body string) (pullRequest, error) {
	return s.addPullCommentAt(repo, id, author, body, "", 0, "")
}

func (s *store) addPullCommentAt(repo string, id int, author, body, file string, line int, side string) (pullRequest, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 20000 {
		return pullRequest{}, errors.New("comment must be between 1 and 20000 characters")
	}
	file = strings.TrimSpace(file)
	if file != "" {
		if line < 1 || line > 1000000 {
			return pullRequest{}, errors.New("comment line must be between 1 and 1000000")
		}
		if side == "" {
			side = "new"
		}
		if side != "new" {
			return pullRequest{}, errors.New("comment side must be new")
		}
		if filepath.IsAbs(file) || strings.HasPrefix(file, "../") || strings.Contains(file, "/../") || strings.Contains(file, "\\") {
			return pullRequest{}, errors.New("invalid comment file path")
		}
	}
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		if file != "" {
			path, err := s.repoPath(repo)
			if err != nil {
				return err
			}
			content, truncated, err := gitOutput(path, 2<<20, "show", pr.HeadSHA+":"+file)
			if err != nil || truncated {
				return errors.New("comment file is not present or is too large")
			}
			if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
				return errors.New("line comments require a text file")
			}
			lineCount := strings.Count(content, "\n")
			if content != "" && !strings.HasSuffix(content, "\n") {
				lineCount++
			}
			if line > lineCount {
				return errors.New("comment line is outside the file")
			}
		}
		commentID := 1
		for _, comment := range pr.Comments {
			if comment.ID >= commentID {
				commentID = comment.ID + 1
			}
		}
		comment := pullComment{ID: commentID, Author: author, Body: body, Path: file, Line: line, Side: side, Commit: pr.HeadSHA, CreatedAt: time.Now().UTC()}
		pr.Comments = append(pr.Comments, comment)
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) approvePullRequest(repo string, id int, user string) (pullRequest, error) {
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		for _, approval := range pr.Approvals {
			if approval.User == user {
				updated = *pr
				return nil
			}
		}
		pr.Approvals = append(pr.Approvals, pullApproval{User: user, CreatedAt: time.Now().UTC()})
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) closePullRequest(repo string, id int, user string, admin bool) (pullRequest, error) {
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		if !admin && pr.Author != user {
			return errors.New("only the author or an admin can close a pull request")
		}
		pr.State = "closed"
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) mergePullRequest(repo string, id int, user string) (pullRequest, error) {
	return s.mergePullRequestWithStrategy(repo, id, user, "ff")
}

func (s *store) mergePullRequestWithStrategy(repo string, id int, user, strategy string) (pullRequest, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "ff"
	}
	if strategy != "ff" && strategy != "squash" && strategy != "merge" {
		return pullRequest{}, errors.New("merge strategy must be ff, squash, or merge")
	}
	var updated pullRequest
	err := s.updatePullRequests(func(db *pullRequestDB) error {
		pr, err := findPullRequest(db, repo, id)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return errors.New("pull request is not open")
		}
		if pr.Draft {
			return errors.New("draft pull request must be marked ready before merging")
		}
		policy, err := s.repoPolicy(repo)
		if err != nil {
			return err
		}
		approvedByOther := 0
		for _, approval := range pr.Approvals {
			if approval.User != pr.Author {
				approvedByOther++
			}
		}
		if approvedByOther < policy.RequiredApprovals {
			return fmt.Errorf("pull request needs %d approval(s) from users other than its author", policy.RequiredApprovals)
		}
		if !s.requiredChecksPass(repo, pr.HeadSHA, policy.RequiredChecks) {
			return fmt.Errorf("required checks have not passed: %s", strings.Join(policy.RequiredChecks, ", "))
		}
		if policy.RequireCodeOwners {
			if passed, ownerErr := s.codeOwnerApprovalsPass(repo, *pr); ownerErr != nil {
				return ownerErr
			} else if !passed {
				return errors.New("required code owner approvals have not been granted")
			}
		}
		path, err := s.repoPath(repo)
		if err != nil {
			return err
		}
		base, err := branchSHA(path, pr.Target)
		if err != nil || base != pr.BaseSHA {
			return errors.New("target branch changed; refresh the pull request")
		}
		head, err := branchSHA(path, pr.Source)
		if err != nil || head != pr.HeadSHA {
			return errors.New("source branch changed; refresh the pull request")
		}
		newHead := pr.HeadSHA
		if strategy == "ff" {
			if execCommand("git", "-C", path, "merge-base", "--is-ancestor", pr.BaseSHA, pr.HeadSHA).Run() != nil {
				return errors.New("pull request is not fast-forwardable")
			}
		} else {
			merged, err := s.createMergeCommit(path, *pr, strategy)
			if err != nil {
				return err
			}
			newHead = merged
		}
		if output, err := execCommandOutput("git", "-C", path, "update-ref", "refs/heads/"+pr.Target, newHead, pr.BaseSHA); err != nil {
			return fmt.Errorf("update target branch: %w: %s", err, output)
		}
		if strategy != "ff" {
			_, _ = execCommandOutput("git", "-C", path, "update-ref", "-d", "refs/trace/merge-temp")
		}
		pr.State = "merged"
		pr.UpdatedAt = time.Now().UTC()
		updated = *pr
		return nil
	})
	return updated, err
}

func (s *store) createMergeCommit(repoPath string, pr pullRequest, strategy string) (string, error) {
	workspace, err := os.MkdirTemp("", "trace-merge-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workspace)
	run := func(args ...string) (string, error) {
		cmd := execCommand("git", append([]string{"-C", workspace}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Trace", "GIT_AUTHOR_EMAIL=trace@localhost", "GIT_COMMITTER_NAME=Trace", "GIT_COMMITTER_EMAIL=trace@localhost")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	clone := execCommand("git", "clone", "--local", "--no-hardlinks", repoPath, workspace)
	if out, err := clone.CombinedOutput(); err != nil {
		return "", fmt.Errorf("prepare merge workspace: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := run("checkout", "-B", "trace-merge-target", pr.BaseSHA); err != nil {
		return "", fmt.Errorf("checkout merge base: %w", err)
	}
	if _, err := run("fetch", "origin", "refs/heads/"+pr.Source+":refs/heads/trace-merge-source"); err != nil {
		return "", fmt.Errorf("fetch merge source: %w", err)
	}
	var mergeArgs []string
	if strategy == "squash" {
		mergeArgs = []string{"merge", "--squash", "trace-merge-source"}
	} else {
		mergeArgs = []string{"merge", "--no-ff", "--no-edit", "-m", pr.Title, "trace-merge-source"}
	}
	if output, err := run(mergeArgs...); err != nil {
		return "", fmt.Errorf("merge pull request: %w: %s", err, output)
	}
	if strategy == "squash" {
		if output, err := run("commit", "-m", pr.Title); err != nil {
			return "", fmt.Errorf("create squash commit: %w: %s", err, output)
		}
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve merged commit: %w", err)
	}
	head = strings.TrimSpace(head)
	if output, err := execCommandOutput("git", "--git-dir", repoPath, "fetch", workspace, "HEAD:refs/trace/merge-temp"); err != nil {
		return "", fmt.Errorf("stage merged commit: %w: %s", err, output)
	}
	return head, nil
}

func (s *store) listPullRequests(repo, state string) ([]pullRequest, error) {
	db, err := s.loadPullRequests()
	if err != nil {
		return nil, err
	}
	prs := make([]pullRequest, 0, len(db.Repos[repo]))
	prs = append(prs, db.Repos[repo]...)
	if state != "" && state != "all" {
		filtered := prs[:0]
		for _, pr := range prs {
			if pr.State == state {
				filtered = append(filtered, pr)
			}
		}
		prs = filtered
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].ID > prs[j].ID })
	return prs, nil
}
