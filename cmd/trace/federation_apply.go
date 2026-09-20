package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
)

func lockMirrorSync(repoPath string) (func(), error) {
	lock, err := os.OpenFile(filepath.Join(repoPath, "trace-sync.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func probeRepositoryRefs(repoPath string) (map[string]string, error) {
	output, truncated, err := gitOutput(repoPath, 1<<20, "for-each-ref", "--format=%(refname)\t%(objectname)", "refs/trace/remote/heads", "refs/trace/remote/tags")
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, errors.New("fetched ref listing exceeds 1 MiB")
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || !validGitObjectID(fields[1]) {
			return nil, errors.New("invalid fetched ref listing")
		}
		ref := fields[0]
		switch {
		case strings.HasPrefix(ref, "refs/trace/remote/heads/"):
			ref = "refs/heads/" + strings.TrimPrefix(ref, "refs/trace/remote/heads/")
		case strings.HasPrefix(ref, "refs/trace/remote/tags/"):
			ref = "refs/tags/" + strings.TrimPrefix(ref, "refs/trace/remote/tags/")
		default:
			return nil, fmt.Errorf("unexpected fetched ref %q", ref)
		}
		refs[ref] = fields[1]
	}
	return refs, nil
}

func verifyFetchedManifestRefs(repoPath string, expected map[string]string) error {
	actual, err := probeRepositoryRefs(repoPath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("fetched refs differ from the signed source manifest; mirror refs were not changed")
	}
	return nil
}

func applySignedProbeRefs(repoPath string, expected, local map[string]string) error {
	keys := make([]string, 0, len(expected))
	for ref := range expected {
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return fmt.Errorf("unsupported signed ref %q", ref)
		}
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	deletions := make([]string, 0)
	for ref, oldSHA := range local {
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return fmt.Errorf("unsupported local ref %q", ref)
		}
		if !validGitObjectID(oldSHA) {
			return fmt.Errorf("invalid local object ID for %s", ref)
		}
		if _, exists := expected[ref]; !exists {
			deletions = append(deletions, ref)
		}
	}
	sort.Strings(deletions)
	var commands bytes.Buffer
	commands.WriteString("start\n")
	for _, ref := range keys {
		newSHA := expected[ref]
		if !validGitObjectID(newSHA) {
			return fmt.Errorf("invalid signed object ID for %s", ref)
		}
		if oldSHA, exists := local[ref]; exists {
			if !validGitObjectID(oldSHA) {
				return fmt.Errorf("invalid local object ID for %s", ref)
			}
			fmt.Fprintf(&commands, "update %s %s %s\n", ref, newSHA, oldSHA)
		} else {
			fmt.Fprintf(&commands, "create %s %s\n", ref, newSHA)
		}
	}
	for _, ref := range deletions {
		fmt.Fprintf(&commands, "delete %s %s\n", ref, local[ref])
	}
	commands.WriteString("prepare\ncommit\n")
	cmd := exec.Command("git", "-C", repoPath, "update-ref", "--stdin")
	cmd.Stdin = &commands
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply signed refs: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}
