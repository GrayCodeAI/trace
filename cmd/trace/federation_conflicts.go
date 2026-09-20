package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type federationConflict struct {
	Repo         string    `json:"repo"`
	Source       string    `json:"source"`
	Ref          string    `json:"ref"`
	LocalSHA     string    `json:"local_sha,omitempty"`
	RemoteSHA    string    `json:"remote_sha,omitempty"`
	DetectedAt   time.Time `json:"detected_at"`
	ApprovedBy   string    `json:"approved_by,omitempty"`
	ApprovedAt   time.Time `json:"approved_at,omitempty"`
	AppliedAt    time.Time `json:"applied_at,omitempty"`
	SupersededAt time.Time `json:"superseded_at,omitempty"`
}

const federationConflictsFile = "federation-conflicts.json"

func (s *store) loadFederationConflicts() ([]federationConflict, error) {
	b, err := os.ReadFile(filepath.Join(s.root, federationConflictsFile))
	if errors.Is(err, os.ErrNotExist) {
		return []federationConflict{}, nil
	}
	if err != nil {
		return nil, err
	}
	var conflicts []federationConflict
	if err := json.Unmarshal(b, &conflicts); err != nil {
		return nil, fmt.Errorf("read federation conflicts: %w", err)
	}
	return conflicts, nil
}

func (s *store) recordFederationConflicts(items []federationConflict) error {
	if len(items) == 0 {
		return nil
	}
	return s.updateFederationConflicts(func(old *[]federationConflict) error {
		for _, item := range items {
			found := false
			for i := range *old {
				previous := &(*old)[i]
				if previous.Repo != item.Repo || previous.Source != item.Source || previous.Ref != item.Ref || !previous.AppliedAt.IsZero() || !previous.SupersededAt.IsZero() {
					continue
				}
				if sameFederationConflict(*previous, item) {
					found = true
					continue
				}
				previous.SupersededAt = time.Now().UTC()
			}
			if !found {
				*old = append(*old, item)
			}
		}
		if len(*old) > 1000 {
			*old = (*old)[len(*old)-1000:]
		}
		return nil
	})
}

func sameFederationConflict(a, b federationConflict) bool {
	return a.Repo == b.Repo && a.Source == b.Source && a.Ref == b.Ref && a.LocalSHA == b.LocalSHA && a.RemoteSHA == b.RemoteSHA
}

func (s *store) updateFederationConflicts(change func(*[]federationConflict) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".federation-conflicts.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	items, err := s.loadFederationConflicts()
	if err != nil {
		return err
	}
	if err := change(&items); err != nil {
		return err
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".federation-conflicts-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, filepath.Join(s.root, federationConflictsFile))
}

func validFederationConflictRef(ref string) bool {
	return (strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/")) && !strings.ContainsAny(ref, " \t\r\n")
}

func (s *store) approveFederationConflict(repo, source, ref, localSHA, remoteSHA, username string) error {
	if !validRepoName(repo) || source == "" || !validFederationConflictRef(ref) || !validGitObjectID(localSHA) || (remoteSHA != "" && !validGitObjectID(remoteSHA)) || username == "" {
		return errors.New("invalid federation conflict approval")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if !isMirror(path) {
		return errors.New("conflict approval requires a read-only mirror")
	}
	unlock, err := lockMirrorSync(path)
	if err != nil {
		return err
	}
	defer unlock()
	local, err := s.repositoryRefs(repo)
	if err != nil {
		return err
	}
	if local[ref] != localSHA {
		return errors.New("mirror ref changed since conflict detection")
	}
	want := federationConflict{Repo: repo, Source: source, Ref: ref, LocalSHA: localSHA, RemoteSHA: remoteSHA}
	return s.updateFederationConflicts(func(items *[]federationConflict) error {
		for i := range *items {
			item := &(*items)[i]
			if item.AppliedAt.IsZero() && item.SupersededAt.IsZero() && sameFederationConflict(*item, want) {
				if !item.ApprovedAt.IsZero() {
					return errors.New("federation conflict already approved")
				}
				item.ApprovedBy = username
				item.ApprovedAt = time.Now().UTC()
				return nil
			}
		}
		return errors.New("matching pending federation conflict not found")
	})
}

func (s *store) completeFederationResolutions(repo, source string, expected, previous map[string]string) error {
	return s.updateFederationConflicts(func(items *[]federationConflict) error {
		for i := range *items {
			item := &(*items)[i]
			if item.Repo != repo || item.Source != source || !item.AppliedAt.IsZero() || !item.SupersededAt.IsZero() {
				continue
			}
			remoteSHA, exists := expected[item.Ref]
			if !item.ApprovedAt.IsZero() && previous[item.Ref] == item.LocalSHA && ((exists && remoteSHA == item.RemoteSHA) || (!exists && item.RemoteSHA == "")) {
				item.AppliedAt = time.Now().UTC()
			} else {
				item.SupersededAt = time.Now().UTC()
			}
		}
		return nil
	})
}

func (s *store) checkFederationConflicts(repo, source string, expected map[string]string) (map[string]string, error) {
	local, err := s.repositoryRefs(repo)
	if err != nil {
		return nil, err
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var conflicts []federationConflict
	recorded, err := s.loadFederationConflicts()
	if err != nil {
		return nil, err
	}
	for ref, localSHA := range local {
		remoteSHA, exists := expected[ref]
		if exists && localSHA == remoteSHA {
			continue
		}
		if exists && strings.HasPrefix(ref, "refs/heads/") && execCommand("git", "-C", path, "merge-base", "--is-ancestor", localSHA, remoteSHA).Run() == nil {
			continue
		}
		conflict := federationConflict{Repo: repo, Source: source, Ref: ref, LocalSHA: localSHA, RemoteSHA: remoteSHA, DetectedAt: time.Now().UTC()}
		approved := false
		for _, item := range recorded {
			if item.AppliedAt.IsZero() && item.SupersededAt.IsZero() && !item.ApprovedAt.IsZero() && sameFederationConflict(item, conflict) {
				approved = true
				break
			}
		}
		if !approved {
			conflicts = append(conflicts, conflict)
		}
	}
	if len(conflicts) == 0 {
		return local, nil
	}
	if err := s.recordFederationConflicts(conflicts); err != nil {
		return nil, fmt.Errorf("record federation conflict: %w", err)
	}
	refs := make([]string, 0, len(conflicts))
	for _, item := range conflicts {
		refs = append(refs, item.Ref)
	}
	sort.Strings(refs)
	return nil, fmt.Errorf("federation conflict on %s; synchronization stopped", strings.Join(refs, ", "))
}

func cleanupFederationProbe(repoPath string) {
	out, err := execCommandOutput("git", "-C", repoPath, "for-each-ref", "--format=%(refname)", "refs/trace/remote")
	if err != nil {
		return
	}
	for _, ref := range strings.Split(strings.TrimSpace(out), "\n") {
		if ref != "" {
			_, _ = execCommandOutput("git", "-C", repoPath, "update-ref", "-d", ref)
		}
	}
}
