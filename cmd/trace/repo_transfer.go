package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// Repository transfer renames a repository and every metadata reference to
// it. The steps are idempotent and recorded in a journal first, so a crash
// part-way through is completed by the next openStore instead of leaving
// stores half renamed.

const transferJournalFile = ".transfer-journal.json"

// Per-repository data directories (data/KIND/OWNER/NAME) that move with the
// repository.
var repoDataDirs = []string{"lfs", "packages", "release-assets"}

// Files that must never be rewritten: the audit ledger is append-only and
// records history under the names that were current at the time.
var transferSkippedFiles = map[string]bool{auditFile: true, oidcConfigFile: true, "rate-state.json": true}

// transferMu serializes transfers within one process.
var transferMu sync.Mutex

type transferJournal struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func (s *store) transferRepo(source, target string) error {
	if !validRepoName(source) || !validRepoName(target) {
		return errors.New("repository names must be OWNER/NAME")
	}
	if source == target {
		return errors.New("source and target repository are the same")
	}
	transferMu.Lock()
	defer transferMu.Unlock()
	if err := s.completePendingTransfer(); err != nil {
		return fmt.Errorf("complete interrupted transfer: %w", err)
	}
	sourcePath, err := s.repoPath(source)
	if err != nil {
		return err
	}
	targetPath, err := s.repoPath(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return errors.New("source repository not found")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return errors.New("target repository already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	journal := transferJournal{Source: source, Target: target}
	b, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.root, transferJournalFile), append(b, '\n')); err != nil {
		return fmt.Errorf("record transfer: %w", err)
	}
	return s.applyTransfer(journal)
}

// completePendingTransfer finishes a transfer that was interrupted.
func (s *store) completePendingTransfer() error {
	b, err := os.ReadFile(filepath.Join(s.root, transferJournalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal transferJournal
	if err := json.Unmarshal(b, &journal); err != nil || !validRepoName(journal.Source) || !validRepoName(journal.Target) {
		return errors.New("invalid transfer journal " + transferJournalFile)
	}
	return s.applyTransfer(journal)
}

func (s *store) applyTransfer(journal transferJournal) error {
	sourcePath, err := s.repoPath(journal.Source)
	if err != nil {
		return err
	}
	targetPath, err := s.repoPath(journal.Target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err == nil {
		if _, err := os.Stat(targetPath); err == nil {
			return fmt.Errorf("both %s and %s exist; resolve manually and remove %s", journal.Source, journal.Target, transferJournalFile)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
			return err
		}
		if err := os.Rename(sourcePath, targetPath); err != nil {
			return fmt.Errorf("transfer repository: %w", err)
		}
	}
	for _, kind := range repoDataDirs {
		if err := s.moveRepoDir(kind, journal.Source, journal.Target); err != nil {
			return fmt.Errorf("transfer %s: %w", kind, err)
		}
	}
	if err := s.rewriteRepoReferences(journal.Source, journal.Target); err != nil {
		return fmt.Errorf("rewrite repository references: %w", err)
	}
	return os.Remove(filepath.Join(s.root, transferJournalFile))
}

// moveRepoDir renames data/KIND/OWNER/NAME for a repository transfer.
func (s *store) moveRepoDir(kind, source, target string) error {
	sourceOwner, sourceName, _ := strings.Cut(source, "/")
	targetOwner, targetName, _ := strings.Cut(target, "/")
	from := filepath.Join(s.root, kind, sourceOwner, sourceName)
	to := filepath.Join(s.root, kind, targetOwner, targetName)
	if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(to); err == nil {
		// Leftovers of an earlier repository with the target name.
		if err := os.RemoveAll(to); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// rewriteRepoReferences renames source to target in every JSON metadata
// store, one store at a time under that store's own lock, replacing each file
// atomically. Only structural references change: keys of repository-keyed
// maps (and "REPO\x00..." keys) and "repo" fields. Free text such as issue
// titles or comment bodies is never touched, nor is the audit ledger.
func (s *store) rewriteRepoReferences(source, target string) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") && !transferSkippedFiles[name] {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	for _, name := range files {
		if err := s.rewriteStoreFile(name, source, target); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s *store) rewriteStoreFile(name, source, target string) error {
	// Every file-backed store locks ".<base>.lock"; the action database is
	// guarded by an in-process mutex.
	unlock, err := lockStoreFile(filepath.Join(s.root, "."+strings.TrimSuffix(name, ".json")+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if name == "actions.json" {
		actionRunMu.Lock()
		defer actionRunMu.Unlock()
	}
	path := filepath.Join(s.root, name)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Contains(b, []byte(source)) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	updated, changed := renameRepoReferences(document, source, target, "")
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// renameRepoReferences walks a decoded JSON value. field is the key under
// which value appears in its parent object.
func renameRepoReferences(value any, source, target, field string) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		changed := false
		out := make(map[string]any, len(v))
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, childChanged := renameRepoReferences(v[key], source, target, key)
			newKey := key
			if key == source {
				newKey = target
			} else if strings.HasPrefix(key, source+"\x00") {
				newKey = target + strings.TrimPrefix(key, source)
			}
			if newKey != key || childChanged {
				changed = true
			}
			out[newKey] = child
		}
		return out, changed
	case []any:
		changed := false
		for i := range v {
			child, childChanged := renameRepoReferences(v[i], source, target, field)
			v[i] = child
			changed = changed || childChanged
		}
		return v, changed
	case string:
		if field == "repo" && v == source {
			return target, true
		}
		return v, false
	default:
		return v, false
	}
}

func lockStoreFile(path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
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

// writeFileAtomic replaces path with data via a synced temporary file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(name, path)
}
