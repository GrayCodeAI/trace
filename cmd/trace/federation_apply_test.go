package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignedProbeRefsMatchBeforeAtomicApply(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/demo", true); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := s.repoPath("team/demo")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	file := filepath.Join(work, "README.md")
	if err := os.WriteFile(file, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "one")
	gitAdminPush(t, work, repoPath, "main")
	before, err := s.repositoryRefs("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	first := before["refs/heads/main"]
	if err := os.WriteFile(file, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "two")
	gitTest(t, t.TempDir(), "-C", repoPath, "fetch", "--no-tags", work, "+refs/heads/main:refs/trace/remote/heads/main")
	probe, err := probeRepositoryRefs(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	second := probe["refs/heads/main"]
	if second == "" || second == first {
		t.Fatalf("probe did not fetch new object: %+v", probe)
	}
	if err := verifyFetchedManifestRefs(repoPath, before); err == nil || !strings.Contains(err.Error(), "mirror refs were not changed") {
		t.Fatalf("mismatched signed refs accepted: %v", err)
	}
	unchanged, _ := s.repositoryRefs("team/demo")
	if unchanged["refs/heads/main"] != first {
		t.Fatal("probe verification changed the published branch")
	}
	if err := applySignedProbeRefs(repoPath, probe, before); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.repositoryRefs("team/demo")
	if updated["refs/heads/main"] != second {
		t.Fatal("signed probe object was not published")
	}
	if err := applySignedProbeRefs(repoPath, probe, before); err == nil {
		t.Fatal("stale ref snapshot was allowed to overwrite a changed branch")
	}
	gitTest(t, t.TempDir(), "-C", repoPath, "update-ref", "refs/tags/v1", first)
	gitTest(t, t.TempDir(), "-C", repoPath, "update-ref", "refs/heads/tags/v1", second)
	refs, err := s.repositoryRefs("team/demo")
	if err != nil || refs["refs/tags/v1"] != first || refs["refs/heads/tags/v1"] != second {
		t.Fatalf("tag and branch refs collided: %+v %v", refs, err)
	}
	expected := map[string]string{"refs/heads/main": second, "refs/heads/tags/v1": second, "refs/tags/v1": second}
	if _, err := s.checkFederationConflicts("team/demo", "https://source.example/git/team/demo.git", expected); err == nil || !strings.Contains(err.Error(), "refs/tags/v1") {
		t.Fatalf("changed tag accepted: %v", err)
	}
	source := "https://source.example/git/team/demo.git"
	if _, err := s.checkFederationConflicts("team/demo", source, expected); err == nil {
		t.Fatal("repeated unresolved tag conflict unexpectedly passed")
	}
	conflicts, err := s.loadFederationConflicts()
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("duplicate conflict record: %+v %v", conflicts, err)
	}
	if err := s.approveFederationConflict("team/demo", source, "refs/tags/v1", second, second, "admin"); err == nil {
		t.Fatal("stale local object ID was approved")
	}
	if err := s.approveFederationConflict("team/demo", source, "refs/tags/v1", first, second, "admin"); err != nil {
		t.Fatal(err)
	}
	local, err := s.checkFederationConflicts("team/demo", source, expected)
	if err != nil {
		t.Fatalf("exact approved tag change was stopped: %v", err)
	}
	if err := applySignedProbeRefs(repoPath, expected, local); err != nil {
		t.Fatal(err)
	}
	if err := s.completeFederationResolutions("team/demo", source, expected, local); err != nil {
		t.Fatal(err)
	}
	refs, err = s.repositoryRefs("team/demo")
	if err != nil || refs["refs/tags/v1"] != second {
		t.Fatalf("approved tag did not update: %+v %v", refs, err)
	}
	conflicts, err = s.loadFederationConflicts()
	if err != nil || conflicts[0].ApprovedBy != "admin" || conflicts[0].AppliedAt.IsZero() {
		t.Fatalf("resolution was not completed: %+v %v", conflicts, err)
	}
	gitTest(t, work, "commit", "--allow-empty", "-m", "third")
	third := strings.TrimSpace(gitTest(t, work, "rev-parse", "HEAD"))
	changedExpected := map[string]string{"refs/heads/main": second, "refs/heads/tags/v1": second, "refs/tags/v1": third}
	if _, err := s.checkFederationConflicts("team/demo", source, changedExpected); err == nil || !strings.Contains(err.Error(), "refs/tags/v1") {
		t.Fatalf("new signed tag target did not require a fresh decision: %v", err)
	}
	deleteExpected := map[string]string{"refs/heads/main": second, "refs/tags/v1": second}
	if _, err := s.checkFederationConflicts("team/demo", source, deleteExpected); err == nil || !strings.Contains(err.Error(), "refs/heads/tags/v1") {
		t.Fatalf("source deletion did not stop sync: %v", err)
	}
	if err := s.approveFederationConflict("team/demo", source, "refs/heads/tags/v1", second, "", "admin"); err != nil {
		t.Fatal(err)
	}
	local, err = s.checkFederationConflicts("team/demo", source, deleteExpected)
	if err != nil {
		t.Fatalf("approved deletion was stopped: %v", err)
	}
	if err := applySignedProbeRefs(repoPath, deleteExpected, local); err != nil {
		t.Fatal(err)
	}
	if err := s.completeFederationResolutions("team/demo", source, deleteExpected, local); err != nil {
		t.Fatal(err)
	}
	refs, err = s.repositoryRefs("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := refs["refs/heads/tags/v1"]; exists {
		t.Fatal("approved source deletion left the mirror branch behind")
	}
	conflicts, err = s.loadFederationConflicts()
	if err != nil || len(conflicts) != 3 || conflicts[1].SupersededAt.IsZero() || conflicts[2].AppliedAt.IsZero() {
		t.Fatalf("stale conflict and applied deletion not recorded: %+v %v", conflicts, err)
	}
}
