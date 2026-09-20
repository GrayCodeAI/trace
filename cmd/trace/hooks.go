package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const receiveHookMarker = "# trace-managed:"

func receiveHook(policy repoPolicy) string {
	patterns := make([]string, 0, len(policy.ProtectedBranches))
	for _, branch := range policy.ProtectedBranches {
		patterns = append(patterns, "refs/heads/"+branch)
	}
	if len(patterns) == 0 {
		patterns = []string{"refs/heads/main"}
	}
	patterns = append(patterns, "refs/tags/*", "refs/trace/*")
	return "#!/bin/sh\n# trace-managed: configurable protected branches and tags\nif [ \"$TRACE_ADMIN\" = \"1\" ]; then\n  exit 0\nfi\nwhile read old new ref; do\n  case \"$ref\" in\n    " + strings.Join(patterns, "|") + ")\n      echo \"Trace: only an administrator may update $ref\" >&2\n      exit 1 ;;\n    refs/heads/*) ;;\n    *)\n      echo \"Trace: unsupported ref $ref\" >&2\n      exit 1 ;;\n  esac\ndone\nexit 0\n"
}

func installHookWithPolicy(repoPath string, policy repoPolicy) error {
	path := filepath.Join(repoPath, "hooks", "pre-receive")
	if old, err := os.ReadFile(path); err == nil {
		if !bytes.Contains(old, []byte(receiveHookMarker)) {
			return fmt.Errorf("custom pre-receive hook at %s; cannot enforce branch protection", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, []byte(receiveHook(policy)), 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func installHook(repoPath string) error {
	return installHookWithPolicy(repoPath, defaultRepoPolicy())
}

func hasManagedHook(repoPath string) bool {
	path := filepath.Join(repoPath, "hooks", "pre-receive")
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(b, []byte(receiveHookMarker)) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&0100 != 0
}

func (s *store) ensureHooks() error {
	owners, err := os.ReadDir(s.repos)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if !owner.IsDir() || !namePattern.MatchString(owner.Name()) {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(s.repos, owner.Name()))
		if err != nil {
			return err
		}
		for _, repo := range repos {
			name := strings.TrimSuffix(repo.Name(), ".git")
			if repo.IsDir() && strings.HasSuffix(repo.Name(), ".git") && namePattern.MatchString(name) {
				if err := installHook(filepath.Join(s.repos, owner.Name(), repo.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
