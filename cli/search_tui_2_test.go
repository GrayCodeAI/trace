package cli

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/GrayCodeAI/trace/cli/search"
)

func TestSearchModel_SelectedResult(t *testing.T) {
	t.Parallel()

	m := testModel()
	r := m.selectedResult()
	if r == nil {
		t.Fatal("selectedResult() = nil, want first result")
		return
	}
	if r.Data.ID != "a3b2c4d5e6f7" {
		t.Errorf("selectedResult().Data.ID = %q, want %q", r.Data.ID, "a3b2c4d5e6f7")
	}

	// Move cursor to second result
	m.cursor = 1
	r = m.selectedResult()
	if r == nil {
		t.Fatal("selectedResult() at cursor 1 = nil")
		return
	}
	if r.Data.ID != "d5e6f789ab01" {
		t.Errorf("selectedResult().Data.ID = %q, want %q", r.Data.ID, "d5e6f789ab01")
	}

	// Out-of-range cursor returns nil
	m.cursor = 99
	if got := m.selectedResult(); got != nil {
		t.Errorf("selectedResult() at cursor 99 = %v, want nil", got)
	}
}

func TestSearchModel_PageNavigation(t *testing.T) {
	t.Parallel()

	// Create model with 30 results (2 pages)
	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{ServiceURL: "http://test", Owner: "o", Repo: "r"}
	results := make([]search.Result, 30)
	for i := range results {
		results[i] = search.Result{Data: search.CheckpointResult{ID: fmt.Sprintf("id-%02d", i)}}
	}
	m := newSearchModel(results, "q", 30, cfg, ss)

	if m.page != 0 {
		t.Fatalf("initial page = %d, want 0", m.page)
	}

	// Navigate to next page
	m = updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.page != 1 {
		t.Errorf("after 'n': page = %d, want 1", m.page)
	}
	if m.cursor != 0 {
		t.Errorf("after 'n': cursor = %d, want 0 (reset)", m.cursor)
	}

	// Can't go past last page
	m = updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.page != 1 {
		t.Errorf("after 'n' on last page: page = %d, want 1", m.page)
	}

	// Navigate back
	m = updateModel(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.page != 0 {
		t.Errorf("after 'p': page = %d, want 0", m.page)
	}

	// Can't go before first page
	m = updateModel(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.page != 0 {
		t.Errorf("after 'p' on first page: page = %d, want 0", m.page)
	}
}

func TestSearchModel_NewSearchClearsFilters(t *testing.T) {
	t.Parallel()

	// Create model with startup filters
	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{
		ServiceURL: "http://test", Owner: "o", Repo: "r", Limit: 25,
		Author: "alice", Date: "week",
	}
	m := newSearchModel(testResults(), "auth", 2, cfg, ss)

	// Enter search mode
	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})

	// Type a query without filters
	m.input.SetValue(newQuery)

	// Press enter — should trigger search with cleared filters
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if !m.loading {
		t.Fatal("expected loading to be true")
	}
	if cmd == nil {
		t.Fatal("expected a search command")
	}

	// searchCfg should be updated with the new query and cleared filters,
	// so that fetchMoreResults uses the correct config for page 2+.
	if m.searchCfg.Author != "" {
		t.Errorf("searchCfg.Author should be cleared, got %q", m.searchCfg.Author)
	}
	if m.searchCfg.Date != "" {
		t.Errorf("searchCfg.Date should be cleared, got %q", m.searchCfg.Date)
	}
	if got := m.searchCfg.Repos; len(got) != 0 {
		t.Errorf("searchCfg.Repos should be cleared, got %v", got)
	}
	if m.searchCfg.Query != newQuery {
		t.Errorf("searchCfg.Query = %q, want %q", m.searchCfg.Query, newQuery)
	}
}

func TestSearchModel_FetchMoreError(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{}
	m := newSearchModel(make([]search.Result, 25), "q", 50, cfg, ss)
	m.fetchingMore = true

	m = updateModel(t, m, searchMoreResultsMsg{err: errTestSearch})

	if m.fetchingMore {
		t.Error("fetchingMore should be false after error")
	}
	if m.searchErr == "" {
		t.Error("searchErr should be set after fetch-more error")
	}
	if len(m.results) != 25 {
		t.Errorf("results should be unchanged, got %d", len(m.results))
	}
}

func TestSearchModel_FetchMoreEmpty_CapsTotal(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{}
	m := newSearchModel(make([]search.Result, 25), "q", 100, cfg, ss)

	if m.totalPages() != 4 {
		t.Fatalf("initial totalPages = %d, want 4", m.totalPages())
	}

	// Simulate API returning empty results (exhausted)
	m = updateModel(t, m, searchMoreResultsMsg{results: nil})

	if m.total != 25 {
		t.Errorf("total should be capped to loaded results (25), got %d", m.total)
	}
	if m.totalPages() != 1 {
		t.Errorf("totalPages should be 1 after cap, got %d", m.totalPages())
	}
}

func TestSearchModel_ViewFetchingMore(t *testing.T) {
	t.Parallel()

	// Model with 25 loaded results but on page 2 (no data) while fetching
	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{}
	m := initTestViewport(newSearchModel(make([]search.Result, 25), "q", 50, cfg, ss))
	m.page = 1
	m.fetchingMore = true
	m = m.refreshBrowseContent()

	view := m.View().Content
	if !strings.Contains(view, "Loading more results...") {
		t.Error("view should show loading message when fetchingMore and page has no data")
	}
}

func TestSearchModel_NewSearchPersistsFilters(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{ServiceURL: "http://test", Owner: "o", Repo: "r", Limit: 25}
	m := newSearchModel(testResults(), "old", 2, cfg, ss)

	// Enter search mode and type query with filters
	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m.input.SetValue(newQuery + " author:bob date:month")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if m.searchCfg.Query != newQuery {
		t.Errorf("searchCfg.Query = %q, want %q", m.searchCfg.Query, newQuery)
	}
	if m.searchCfg.Author != "bob" {
		t.Errorf("searchCfg.Author = %q, want %q", m.searchCfg.Author, "bob")
	}
	if m.searchCfg.Date != "month" {
		t.Errorf("searchCfg.Date = %q, want %q", m.searchCfg.Date, "month")
	}
}

func TestSearchModel_NewSearchPersistsRepoFilters(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{
		ServiceURL: "http://test",
		Owner:      "default-owner",
		Repo:       "default-repo",
		Limit:      25,
	}
	m := newSearchModel(testResults(), "old", 2, cfg, ss)

	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m.input.SetValue(newQuery + " repo:GrayCodeAI/trace.io")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if m.searchCfg.Query != newQuery {
		t.Errorf("searchCfg.Query = %q, want %q", m.searchCfg.Query, newQuery)
	}
	if got := m.searchCfg.Repos; len(got) != 1 || got[0] != "GrayCodeAI/trace.io" {
		t.Errorf("searchCfg.Repos = %v, want %v", got, []string{"GrayCodeAI/trace.io"})
	}
}

func TestSearchModel_NewSearchClearsExplicitRepoFilters(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{
		ServiceURL: "http://test",
		Owner:      "default-owner",
		Repo:       "default-repo",
		Limit:      25,
		Repos:      []string{"GrayCodeAI/trace.io"},
	}
	m := newSearchModel(testResults(), "auth", 2, cfg, ss)

	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m.input.SetValue(newQuery)

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if got := m.searchCfg.Repos; len(got) != 0 {
		t.Errorf("searchCfg.Repos = %v, want empty explicit repo overrides", got)
	}
	if m.searchCfg.Owner != "default-owner" || m.searchCfg.Repo != "default-repo" {
		t.Errorf("default repo scope changed unexpectedly: %s/%s", m.searchCfg.Owner, m.searchCfg.Repo)
	}
}

func TestSearchModel_NewSearchAllReposFilter(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{
		ServiceURL: "http://test",
		Owner:      "default-owner",
		Repo:       "default-repo",
		Limit:      25,
	}
	m := newSearchModel(testResults(), "old", 2, cfg, ss)

	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m.input.SetValue(newQuery + " repo:*")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if got := m.searchCfg.Repos; len(got) != 1 || got[0] != search.AllReposFilter {
		t.Errorf("searchCfg.Repos = %v, want %v", got, []string{search.AllReposFilter})
	}
}

func TestSearchModel_NewSearchRejectsMultipleExplicitRepos(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{
		ServiceURL: "http://test",
		Owner:      "default-owner",
		Repo:       "default-repo",
		Limit:      25,
	}
	m := newSearchModel(testResults(), "old", 2, cfg, ss)

	m = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m.input.SetValue(newQuery + " repo:GrayCodeAI/trace.io,GrayCodeAI/cli")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, ok := updated.(searchModel)
	if !ok {
		t.Fatalf("Update returned %T, want searchModel", updated)
	}

	if cmd != nil {
		t.Fatal("expected no search command on invalid multi-repo input")
	}
	if m.mode != modeSearch {
		t.Errorf("mode = %d, want modeSearch", m.mode)
	}
	if m.searchErr != "only one explicit repo filter is currently supported" {
		t.Errorf("searchErr = %q", m.searchErr)
	}
}

func TestSearchModel_ApiPageInitialization(t *testing.T) {
	t.Parallel()

	ss := statusStyles{colorEnabled: false, width: 100}
	cfg := search.Config{}

	// With results: apiPage = 1
	withResults := newSearchModel(testResults(), "q", 2, cfg, ss)
	if withResults.apiPage != 1 {
		t.Errorf("apiPage with results = %d, want 1", withResults.apiPage)
	}

	// Without results: apiPage = 0
	noResults := newSearchModel(nil, "", 0, cfg, ss)
	if noResults.apiPage != 0 {
		t.Errorf("apiPage without results = %d, want 0", noResults.apiPage)
	}
}

func TestComputeColumns(t *testing.T) {
	t.Parallel()

	cols := computeColumns(100)
	if cols.age != 10 {
		t.Errorf("age width = %d, want 10", cols.age)
	}
	if cols.id != 12 {
		t.Errorf("id width = %d, want 12", cols.id)
	}
	if cols.repo < 10 {
		t.Errorf("repo width = %d, want >= 10", cols.repo)
	}
	if cols.author != 14 {
		t.Errorf("author width = %d, want 14", cols.author)
	}

	cols = computeColumns(40)
	if cols.branch < 8 {
		t.Errorf("branch width on narrow terminal = %d, want >= 8", cols.branch)
	}
	if cols.repo < 10 {
		t.Errorf("repo width on narrow terminal = %d, want >= 10", cols.repo)
	}
}
