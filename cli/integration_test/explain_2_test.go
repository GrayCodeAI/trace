//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cli/execx"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// TestExplain_CheckpointV2SucceedsAfterTreelessFetch is the v2 mirror —
// guards V2GitStore's read path against the same blob-missing regression.
// Required because v2 will be enabled by default soon and reaches the
// same Tree.File() trap as v1.
func TestExplain_CheckpointV2SucceedsAfterTreelessFetch(t *testing.T) {
	t.Parallel()
	env := NewFeatureBranchEnv(t)

	env.PatchSettings(map[string]any{
		"strategy_options": map[string]any{
			"checkpoints_v2": true,
			"push_v2_refs":   true,
		},
	})

	bareURL := env.SetupBareRemote()
	checkpointID := createAndPushCheckpoint(t, env, "treeless_v2.go", "Treeless v2 prompt")

	cloneDir := setupTreelessClone(t, bareURL, "+"+paths.V2MainRefName+":"+paths.V2MainRefName)
	writeV2Settings(t, cloneDir)
	requireBlobMissing(t, cloneDir, checkpointID, true /* v2 */)

	output := runExplainInDir(t, cloneDir, checkpointID)
	require.Contains(t, output, "Treeless v2 prompt",
		"explain should succeed against v2 with blobs absent locally")
}

// createAndPushCheckpoint runs a session-create-stop cycle in env and
// pushes the resulting checkpoint to origin. Returns the checkpoint ID.
func createAndPushCheckpoint(t *testing.T, env *TestEnv, fileName, prompt string) string {
	t.Helper()
	session := env.NewSession()
	transcriptPath := session.CreateTranscript(prompt, []FileChange{
		{Path: fileName, Content: "package treeless"},
	})
	require.NoError(t, env.SimulateUserPromptSubmitWithPromptAndTranscriptPath(session.ID, prompt, transcriptPath))
	env.WriteFile(fileName, "package treeless")
	env.GitAdd(fileName)
	require.NoError(t, env.SimulateStop(session.ID, transcriptPath))
	env.GitCommitWithShadowHooks("Add "+fileName, fileName)
	cpID := env.GetLatestCheckpointID()
	require.NotEmpty(t, cpID, "expected a checkpoint after condensation")
	env.RunPrePush("origin")
	return cpID
}

// setupTreelessClone creates a fresh git repo in a fresh TempDir, fetches
// the given refspec from bareURL with --filter=blob:none --depth=1 (so
// trees but no blobs land locally), and writes a minimal trace settings
// file pointing at bareURL as the checkpoint_remote. Returns the new dir.
//
// Note: the bare and the fetch must go through the smart protocol for
// --filter to be honored; the default local-path transport optimization
// copies packs verbatim and ignores filters. We set
// uploadpack.allowFilter=true on the bare and use a file:// URL with
// protocol.file.allow=always to force the smart path.
func setupTreelessClone(t *testing.T, barePath, refspec string) string {
	t.Helper()
	gitEnv := testutil.GitIsolatedEnv()
	enableFilterOnBare(t, barePath, gitEnv)

	cloneDir := t.TempDir()
	fileURL := "file://" + barePath

	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "protocol.file.allow=always", "fetch", "--filter=blob:none", "--depth=1", "--no-tags", fileURL, refspec},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = cloneDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	require.NoError(t, writeMinimalTraceSettings(cloneDir, barePath))
	return cloneDir
}

// enableFilterOnBare sets uploadpack.allowFilter=true on the bare repo so
// that --filter=blob:none on fetch is honored.
func enableFilterOnBare(t *testing.T, barePath string, gitEnv []string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "-C", barePath, "config", "uploadpack.allowFilter", "true")
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to set uploadpack.allowFilter on bare: %v\n%s", err, out)
	}
	cmd = exec.CommandContext(t.Context(), "git", "-C", barePath, "config", "uploadpack.allowAnySHA1InWant", "true")
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to set uploadpack.allowAnySHA1InWant on bare: %v\n%s", err, out)
	}
}

// writeMinimalTraceSettings writes the smallest valid settings.json that
// configures the manual-commit strategy with filtered_fetches enabled and
// a custom checkpoint_remote URL — the partial-clone setup that triggered
// the original bug.
func writeMinimalTraceSettings(dir, bareURL string) error {
	traceDir := filepath.Join(dir, ".trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		return err
	}
	settings := map[string]any{
		"enabled":   true,
		"local_dev": true,
		"strategy":  "manual-commit",
		"strategy_options": map[string]any{
			"filtered_fetches": true,
			"checkpoint_remote": map[string]any{
				"provider": "url",
				"url":      bareURL,
			},
		},
	}
	data, err := jsonutil.MarshalIndentWithNewline(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(traceDir, paths.SettingsFileName), data, 0o644)
}

// writeV2Settings overlays checkpoints_v2 enablement on the settings written
// by writeMinimalTraceSettings.
func writeV2Settings(t *testing.T, dir string) {
	t.Helper()
	settingsPath := filepath.Join(dir, ".trace", paths.SettingsFileName)
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	opts, _ := settings["strategy_options"].(map[string]any)
	opts["checkpoints_v2"] = true
	settings["strategy_options"] = opts

	updated, err := jsonutil.MarshalIndentWithNewline(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(settingsPath, updated, 0o644))
}

// runExplainInDir runs `trace explain --checkpoint <id>` in dir and
// returns combined output. Fails the test if the command errors. Uses
// execx.NonInteractive (project rule for spawning the trace binary in
// tests) so the child has no controlling terminal.
func runExplainInDir(t *testing.T, dir, checkpointID string) string {
	t.Helper()
	cmd := execx.NonInteractive(t.Context(), getTestBinary(), "explain", "--checkpoint", checkpointID)
	cmd.Dir = dir
	cmd.Env = testutil.GitIsolatedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("explain failed: %v\n%s", err, out)
	}
	return string(out)
}

// requireBlobMissing asserts that at least one metadata blob for the
// checkpoint is genuinely absent from the local object store. Confirms the
// treeless-clone setup actually reproduces the bug-triggering state — if
// every blob were locally available, the test would pass without
// exercising the fix.
func requireBlobMissing(t *testing.T, dir, checkpointID string, isV2 bool) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	var ref *plumbing.Reference
	if isV2 {
		ref, err = repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	} else {
		ref, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	}
	require.NoError(t, err, "metadata ref should exist after treeless fetch")

	commit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	rootTree, err := commit.Tree()
	require.NoError(t, err)
	cpSubtree, err := rootTree.Tree(checkpointID[:2] + "/" + checkpointID[2:])
	require.NoError(t, err, "cp subtree should be navigable from local trees")

	for _, entry := range cpSubtree.Entries {
		if !entry.Mode.IsFile() {
			continue
		}
		if _, err := repo.BlobObject(entry.Hash); err != nil {
			return // confirmed: at least one blob is missing
		}
	}
	t.Fatalf("expected at least one metadata blob to be missing in fresh treeless clone (cp=%s, v2=%v)", checkpointID, isV2)
}
