package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/trail"
)

const (
	trailListTestAuthorAlice = "alice"
	trailListTestAuthorBob   = "bob"
)

func TestParseTrailStatusFilterAcceptsCommaSeparatedStatuses(t *testing.T) {
	t.Parallel()
	got, err := parseTrailStatusFilter("in_progress, open,closed")
	if err != nil {
		t.Fatalf("parseTrailStatusFilter: %v", err)
	}
	want := []trail.Status{trail.StatusInProgress, trail.StatusOpen, trail.StatusClosed}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseTrailStatusFilterRejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	if _, err := parseTrailStatusFilter("in_progress,nope"); err == nil {
		t.Fatal("expected invalid status error")
	}
}

func TestParseTrailStatusFilterAnySentinelMeansNoFilter(t *testing.T) {
	t.Parallel()
	got, err := parseTrailStatusFilter(trailListStatusAny)
	if err != nil {
		t.Fatalf("parseTrailStatusFilter(%q): %v", trailListStatusAny, err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil (any disables the filter)", got)
	}
}

func TestPrintTrailListDefaultRepoShapeShowsAuthor(t *testing.T) {
	t.Parallel()
	alice := trailListTestAuthorAlice
	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{
			Branch:    "feat/repo-wide",
			Status:    trail.StatusInProgress,
			Author:    &trail.Author{Login: &alice},
			UpdatedAt: time.Now(),
		},
	}, trailListDisplayOptions{
		RequestedAuthor: "",
		StatusFilters:   []trail.Status{trail.StatusInProgress},
	})

	text := out.String()
	for _, want := range []string{"In progress · 1 trail", "feat/repo-wide", trailListTestAuthorAlice} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q, got:\n%s", want, text)
		}
	}
}

func TestPrintTrailListAuthorFilteredShapeHidesAuthor(t *testing.T) {
	t.Parallel()
	longBranch := "feature/very-long-branch-name-that-must-remain-visible"
	alice := trailListTestAuthorAlice

	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{
			Branch:    longBranch,
			Status:    trail.StatusInProgress,
			Author:    &trail.Author{Login: &alice},
			UpdatedAt: time.Now().Add(-24 * time.Hour),
		},
	}, trailListDisplayOptions{
		RequestedAuthor: trailListTestAuthorAlice,
		StatusFilters:   []trail.Status{trail.StatusInProgress},
	})

	text := out.String()
	if !strings.Contains(text, "alice · 1 in progress") {
		t.Fatalf("output should contain author/status header, got:\n%s", text)
	}
	if !strings.Contains(text, longBranch) {
		t.Fatalf("output should contain full branch %q, got:\n%s", longBranch, text)
	}
	if strings.Count(text, "alice") != 1 {
		t.Fatalf("filtered author output should not repeat author in rows, got:\n%s", text)
	}
}

func TestPrintTrailListYourTrailsRelabelsAndSurfacesGhLogin(t *testing.T) {
	t.Parallel()
	mixedCase := "Alice" // gh returned a different case than the filter
	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{
			Branch:    "feat/x",
			Status:    trail.StatusInProgress,
			Author:    &trail.Author{Login: &mixedCase},
			UpdatedAt: time.Now(),
		},
	}, trailListDisplayOptions{
		RequestedAuthor: "alice",
		CurrentUser:     "alice",
		StatusFilters:   []trail.Status{trail.StatusInProgress},
	})

	text := out.String()
	if !strings.Contains(text, "Your trails (alice) · 1 in progress") {
		t.Fatalf("expected 'Your trails (alice)' header, got:\n%s", text)
	}
}

func TestPrintTrailListSingularRecentTrailWhenOne(t *testing.T) {
	t.Parallel()
	alice := trailListTestAuthorAlice
	var out bytes.Buffer
	printTrailList(&out, []*trail.Metadata{
		{Branch: "feat/a", Status: trail.StatusInProgress, Author: &trail.Author{Login: &alice}, UpdatedAt: time.Now()},
	}, trailListDisplayOptions{
		RequestedAuthor: "",
		StatusFilters:   nil,
	})

	text := out.String()
	if !strings.Contains(text, "Recent trail · 1") {
		t.Fatalf("expected singular 'Recent trail · 1', got:\n%s", text)
	}
	if strings.Contains(text, "Recent trails · 1") {
		t.Fatalf("did not expect plural 'trails' for count 1, got:\n%s", text)
	}
}

func TestFetchCurrentUserLoginReturnsLogin(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "octocat\n", nil)

	got, err := fetchCurrentUserLogin(context.Background(), r)
	if err != nil {
		t.Fatalf("fetchCurrentUserLogin: %v", err)
	}
	if got != "octocat" {
		t.Fatalf("got %q, want octocat", got)
	}
}

func TestFetchCurrentUserLoginRejectsEmptyLogin(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "\n", nil)

	if _, err := fetchCurrentUserLogin(context.Background(), r); err == nil {
		t.Fatal("expected error for empty login")
	}
}

func TestFetchCurrentUserLoginWrapsGhError(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "", errors.New("gh: not authenticated"))

	_, err := fetchCurrentUserLogin(context.Background(), r)
	if err == nil {
		t.Fatal("expected error")
	}
	// Surface the hint about the --author <login> fallback.
	if !strings.Contains(err.Error(), "--author <login>") {
		t.Fatalf("error should mention the --author fallback hint, got: %v", err)
	}
}
