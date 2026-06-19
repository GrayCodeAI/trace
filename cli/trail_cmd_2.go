package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/api"
	"github.com/GrayCodeAI/trace/cli/trail"

	"charm.land/huh/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

func formatValidStatuses() string {
	statuses := trail.ValidStatuses()
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// runTrailCreateInteractive runs the interactive form for trail creation.
// Prompts for title, body, branch (derived from title), and status.
func runTrailCreateInteractive(title, body, branch, statusStr *string) error {
	// Step 1: Title and body
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Trail title").
				Placeholder("What are you working on?").
				Value(title),
			huh.NewText().
				Title("Body (optional)").
				Value(body),
		),
	)
	if err := form.Run(); err != nil {
		return fmt.Errorf("form cancelled: %w", err)
	}
	*title = strings.TrimSpace(*title)
	if *title == "" {
		return errors.New("trail title is required")
	}

	// Step 2: Branch (derived from title) and status
	suggested := slugifyTitle(*title)
	*branch = suggested

	// Build status options, excluding done/closed
	var statusOptions []huh.Option[string]
	for _, s := range trail.ValidStatuses() {
		if s == trail.StatusMerged || s == trail.StatusClosed {
			continue
		}
		statusOptions = append(statusOptions, huh.NewOption(string(s), string(s)))
	}
	if *statusStr == "" {
		*statusStr = string(trail.StatusDraft)
	}

	form = NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Branch name").
				Placeholder(suggested).
				Value(branch),
			huh.NewSelect[string]().
				Title("Status").
				Options(statusOptions...).
				Value(statusStr),
		),
	)
	if err := form.Run(); err != nil {
		return fmt.Errorf("form cancelled: %w", err)
	}
	*branch = strings.TrimSpace(*branch)
	if *branch == "" {
		*branch = suggested
	}
	return nil
}

// findTrailByBranch looks up a trail by branch name via the list API.
func findTrailByBranch(ctx context.Context, client *api.Client, host, owner, repo, branch string) (*api.TrailResource, error) {
	return findTrail(ctx, client, host, owner, repo, func(t api.TrailResource) bool {
		return t.Branch == branch
	})
}

func findTrail(ctx context.Context, client *api.Client, host, owner, repo string, match func(api.TrailResource) bool) (*api.TrailResource, error) {
	resp, err := client.Get(ctx, trailsBasePath(host, owner, repo))
	if err != nil {
		return nil, fmt.Errorf("list trails: %w", err)
	}
	defer resp.Body.Close()
	if err := checkTrailResponse(resp); err != nil {
		return nil, err
	}

	var listResp api.TrailListResponse
	if err := api.DecodeJSON(resp, &listResp); err != nil {
		return nil, fmt.Errorf("decode trail list: %w", err)
	}

	for i := range listResp.Trails {
		if match(listResp.Trails[i]) {
			return &listResp.Trails[i], nil
		}
	}
	return nil, nil //nolint:nilnil // nil, nil means "not found" — callers check both
}

// apiHostAlias maps git host domains to short aliases used by the trails API.
var apiHostAlias = map[string]string{
	"github.com": "gh",
}

// trailsBasePath returns the API path prefix for trails endpoints (e.g., "/api/v1/trails/gh/org/repo").
func trailsBasePath(host, owner, repo string) string {
	if alias, ok := apiHostAlias[host]; ok {
		host = alias
	}
	return fmt.Sprintf("/api/v1/trails/%s/%s/%s", host, owner, repo)
}

// checkTrailResponse checks the API response and returns user-friendly errors.
// For auth failures, it appends a hint to re-authenticate while preserving the server's error message.
func checkTrailResponse(resp *http.Response) error {
	if err := api.CheckResponse(resp); err != nil {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w — run 'trace login' to re-authenticate", err)
		}
		return fmt.Errorf("trail API: %w", err)
	}
	return nil
}

// slugifyTitle converts a title string into a branch-friendly slug.
// Example: "Add user authentication" -> "add-user-authentication"
func slugifyTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	// Replace spaces and underscores with hyphens
	s = strings.NewReplacer(" ", "-", "_", "-").Replace(s)
	// Remove anything that's not alphanumeric, hyphen, or slash
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '/' {
			b.WriteRune(r)
			prevHyphen = false
		} else if r == '-' && !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// branchNeedsCreation checks if a branch exists locally.
func branchNeedsCreation(repo *git.Repository, branchName string) bool {
	_, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	return err != nil
}

// createBranch creates a new local branch pointing at HEAD without checking it out.
func createBranch(repo *git.Repository, branchName string) error {
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), head.Hash())
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to create branch ref: %w", err)
	}
	return nil
}

// pushBranchToOrigin pushes a branch to the origin remote.
func pushBranchToOrigin(branchName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "push", "--no-verify", "-u", "origin", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
