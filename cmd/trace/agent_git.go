package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

const agentGitRefPrefix = "refs/trace/agent-bundles/"
const agentGitBundleFile = "bundle.json"

var errAgentGitRefMissing = errors.New("Git agent bundle ref not found")

type agentGitResult struct {
	Ref      string `json:"ref"`
	Commit   string `json:"commit,omitempty"`
	NodeID   string `json:"node_id"`
	Sessions int    `json:"sessions"`
	Changed  bool   `json:"changed"`
}

func (s *store) agentGitStatus(repo string) (agentGitResult, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return agentGitResult{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return agentGitResult{}, errors.New("repository not found")
	}
	private, err := loadOrCreateNodeSigner(s.root)
	if err != nil {
		return agentGitResult{}, err
	}
	nodeID := base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	ref, err := agentGitRef(nodeID)
	if err != nil {
		return agentGitResult{}, err
	}
	result := agentGitResult{Ref: ref, NodeID: nodeID}
	bundle, commit, err := readAgentGitBundle(path, ref, nodeID, repo)
	if errors.Is(err, errAgentGitRefMissing) {
		return result, nil
	}
	if err != nil {
		return agentGitResult{}, err
	}
	result.Commit, result.Sessions = commit, len(bundle.Payload.Sessions)
	return result, nil
}

func agentGitRef(nodeID string) (string, error) {
	if !validFederationNodeID(nodeID) {
		return "", errors.New("valid node ID required")
	}
	digest := sha256.Sum256([]byte(nodeID))
	return agentGitRefPrefix + hex.EncodeToString(digest[:]), nil
}

func gitInput(path string, input []byte, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), env...)
	var output cappedBuffer
	output.limit = 4096
	var stderr cappedBuffer
	stderr.limit = 4096
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	if output.truncated {
		return "", errors.New("git object command output exceeds limit")
	}
	return strings.TrimSpace(output.String()), nil
}

func readAgentGitBundle(path, ref, nodeID, repo string) (signedAgentBundle, string, error) {
	commit, err := gitInput(path, nil, nil, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return signedAgentBundle{}, "", errAgentGitRefMissing
		}
		return signedAgentBundle{}, "", err
	}
	commit = strings.TrimSpace(commit)
	if !isFullHexSHA(commit) {
		return signedAgentBundle{}, "", errors.New("invalid Git agent bundle ref")
	}
	raw, truncated, err := gitOutput(path, maxAgentBundleBytes+64<<10, "show", ref+":"+agentGitBundleFile)
	if err != nil || truncated {
		return signedAgentBundle{}, "", errors.New("cannot read Git agent bundle")
	}
	var bundle signedAgentBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return signedAgentBundle{}, "", errors.New("invalid Git agent bundle JSON")
	}
	if err := verifyAgentBundle(bundle, nodeID, repo); err != nil {
		return signedAgentBundle{}, "", err
	}
	return bundle, commit, nil
}

// publishAgentGitBundle writes a signed, redacted snapshot under this node's
// protected Git ref. Repository readers can fetch the ref, so publication is
// deliberately limited to private repositories.
func (s *store) publishAgentGitBundle(repo string) (agentGitResult, error) {
	bundle, err := s.exportAgentBundle(repo)
	if err != nil {
		return agentGitResult{}, err
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return agentGitResult{}, err
	}
	if isPublic(path) {
		return agentGitResult{}, errors.New("Git agent history cannot be published in a public repository")
	}
	if isMirror(path) || isArchived(path) {
		return agentGitResult{}, errors.New("Git agent history cannot be published in a mirror or archived repository")
	}
	ref, err := agentGitRef(bundle.Payload.NodeID)
	if err != nil {
		return agentGitResult{}, err
	}
	result := agentGitResult{Ref: ref, NodeID: bundle.Payload.NodeID, Sessions: len(bundle.Payload.Sessions)}
	oldBundle, oldCommit, err := readAgentGitBundle(path, ref, bundle.Payload.NodeID, repo)
	if err == nil {
		if reflect.DeepEqual(oldBundle.Payload.Sessions, bundle.Payload.Sessions) {
			result.Commit = oldCommit
			return result, nil
		}
	} else if !errors.Is(err, errAgentGitRefMissing) {
		return agentGitResult{}, fmt.Errorf("existing Git agent history must be inspected before publishing: %w", err)
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil || len(encoded) > maxAgentBundleBytes+64<<10 {
		return agentGitResult{}, errors.New("signed Git agent bundle exceeds size limit")
	}
	blob, err := gitInput(path, append(encoded, '\n'), nil, "hash-object", "-w", "--stdin")
	if err != nil || !isFullHexSHA(blob) {
		return agentGitResult{}, errors.New("cannot write Git agent bundle blob")
	}
	treeLine := "100644 blob " + blob + "\t" + agentGitBundleFile + "\n"
	tree, err := gitInput(path, []byte(treeLine), nil, "mktree")
	if err != nil || !isFullHexSHA(tree) {
		return agentGitResult{}, errors.New("cannot write Git agent bundle tree")
	}
	args := []string{"commit-tree", tree, "-m", "Trace signed agent history snapshot"}
	oldValue := strings.Repeat("0", 40)
	if oldCommit != "" {
		args = append(args, "-p", oldCommit)
		oldValue = oldCommit
	}
	identity := []string{"GIT_AUTHOR_NAME=Trace", "GIT_AUTHOR_EMAIL=trace@localhost", "GIT_COMMITTER_NAME=Trace", "GIT_COMMITTER_EMAIL=trace@localhost"}
	commit, err := gitInput(path, nil, identity, args...)
	if err != nil || !isFullHexSHA(commit) {
		return agentGitResult{}, errors.New("cannot write Git agent bundle commit")
	}
	if _, _, err := gitOutput(path, 4096, "update-ref", ref, commit, oldValue); err != nil {
		return agentGitResult{}, fmt.Errorf("Git agent history changed concurrently; retry publish: %w", err)
	}
	result.Commit, result.Changed = commit, true
	return result, nil
}

func (s *store) importAgentGitBundle(repo, expectedNodeID string) (agentBundleImportResult, error) {
	ref, err := agentGitRef(expectedNodeID)
	if err != nil {
		return agentBundleImportResult{}, err
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return agentBundleImportResult{}, err
	}
	bundle, _, err := readAgentGitBundle(path, ref, expectedNodeID, repo)
	if err != nil {
		return agentBundleImportResult{}, err
	}
	return s.importAgentBundle(repo, expectedNodeID, bundle)
}

func (s *store) unpublishAgentGitBundle(repo, expectedNodeID string) (agentGitResult, error) {
	bundle, err := s.exportAgentBundle(repo)
	if err != nil {
		return agentGitResult{}, err
	}
	if bundle.Payload.NodeID != expectedNodeID {
		return agentGitResult{}, errors.New("local node ID does not match expected node ID")
	}
	ref, err := agentGitRef(bundle.Payload.NodeID)
	if err != nil {
		return agentGitResult{}, err
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return agentGitResult{}, err
	}
	_, commit, err := readAgentGitBundle(path, ref, bundle.Payload.NodeID, repo)
	if err != nil {
		return agentGitResult{}, err
	}
	if _, _, err := gitOutput(path, 4096, "update-ref", "-d", ref, commit); err != nil {
		return agentGitResult{}, fmt.Errorf("cannot remove Git agent bundle ref: %w", err)
	}
	return agentGitResult{Ref: ref, NodeID: bundle.Payload.NodeID, Sessions: len(bundle.Payload.Sessions), Changed: true}, nil
}
