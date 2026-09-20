package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const releaseFile = "releases.json"

const maxReleaseAssetSize = 100 << 20

type releaseAsset struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

type release struct {
	ID         int            `json:"id"`
	Repo       string         `json:"repo"`
	Tag        string         `json:"tag"`
	Name       string         `json:"name"`
	Body       string         `json:"body,omitempty"`
	Author     string         `json:"author"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Commit     string         `json:"commit"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Assets     []releaseAsset `json:"assets,omitempty"`
}

func validReleaseAssetName(name string) bool {
	return name != "" && len(name) <= 128 && filepath.Base(name) == name && name != "." && name != ".." && !strings.ContainsAny(name, "\\/")
}

func (s *store) releaseAssetPath(repo string, releaseID int, name string) (string, error) {
	if !validRepoName(repo) || releaseID < 1 || !validReleaseAssetName(name) {
		return "", errors.New("invalid release asset")
	}
	parts := strings.SplitN(repo, "/", 2)
	return filepath.Join(s.root, "release-assets", parts[0], parts[1], strconv.Itoa(releaseID), name), nil
}

func (s *store) listReleaseAssets(repo string, releaseID int) ([]releaseAsset, error) {
	item, err := s.findRelease(repo, releaseID)
	if err != nil {
		return nil, err
	}
	assets := append([]releaseAsset(nil), item.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	return assets, nil
}

func (s *store) addReleaseAsset(repo string, releaseID int, name, contentType string, r io.Reader) (releaseAsset, error) {
	if !validReleaseAssetName(name) {
		return releaseAsset{}, errors.New("asset name must be a single path-safe filename of 1-128 characters")
	}
	if _, err := s.findRelease(repo, releaseID); err != nil {
		return releaseAsset{}, err
	}
	path, err := s.releaseAssetPath(repo, releaseID, name)
	if err != nil {
		return releaseAsset{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return releaseAsset{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".asset-")
	if err != nil {
		return releaseAsset{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return releaseAsset{}, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(r, maxReleaseAssetSize+1))
	if err != nil {
		tmp.Close()
		return releaseAsset{}, err
	}
	if written > maxReleaseAssetSize {
		tmp.Close()
		return releaseAsset{}, errors.New("release asset exceeds 100 MiB")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return releaseAsset{}, err
	}
	if err := tmp.Close(); err != nil {
		return releaseAsset{}, err
	}
	var created releaseAsset
	if err := os.Link(tmpName, path); err != nil {
		if os.IsExist(err) {
			return releaseAsset{}, errors.New("release asset already exists")
		}
		return releaseAsset{}, err
	}
	err = s.updateReleases(func(db *releaseDB) error {
		items := db.Repos[repo]
		for i := range items {
			if items[i].ID != releaseID {
				continue
			}
			for _, asset := range items[i].Assets {
				if asset.Name == name {
					return errors.New("release asset already exists")
				}
			}
			id := 1
			for _, asset := range items[i].Assets {
				if asset.ID >= id {
					id = asset.ID + 1
				}
			}
			created = releaseAsset{ID: id, Name: name, Size: written, ContentType: strings.TrimSpace(contentType), SHA256: fmt.Sprintf("%x", hash.Sum(nil)), CreatedAt: time.Now().UTC()}
			items[i].Assets = append(items[i].Assets, created)
			db.Repos[repo] = items
			return nil
		}
		return errors.New("release not found")
	})
	if err != nil {
		_ = os.Remove(path)
		return releaseAsset{}, err
	}
	return created, nil
}

type releaseDB struct {
	Repos map[string][]release `json:"repos"`
}

func (s *store) loadReleases() (releaseDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, releaseFile))
	if errors.Is(err, os.ErrNotExist) {
		return releaseDB{Repos: make(map[string][]release)}, nil
	}
	if err != nil {
		return releaseDB{}, err
	}
	var db releaseDB
	if err := json.Unmarshal(b, &db); err != nil {
		return releaseDB{}, fmt.Errorf("read releases: %w", err)
	}
	if db.Repos == nil {
		db.Repos = make(map[string][]release)
	}
	return db, nil
}

func (s *store) updateReleases(change func(*releaseDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".releases.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadReleases()
	if err != nil {
		return err
	}
	if err := change(&db); err != nil {
		return err
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".releases-*")
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
	return os.Rename(name, filepath.Join(s.root, releaseFile))
}

func tagCommit(repoPath, tag string) (string, error) {
	if tag == "" || strings.HasPrefix(tag, "-") || strings.Contains(tag, "..") || strings.ContainsAny(tag, " ~^:?*[\\") {
		return "", errors.New("invalid tag name")
	}
	if execCommand("git", "-C", repoPath, "check-ref-format", "refs/tags/"+tag).Run() != nil {
		return "", errors.New("invalid tag name")
	}
	out, err := execCommandOutput("git", "-C", repoPath, "rev-parse", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return "", errors.New("tag not found")
	}
	return strings.TrimSpace(out), nil
}

func (s *store) createRelease(repo, tag, name, body, author string, draft, prerelease bool) (release, error) {
	if len(name) > 200 || strings.TrimSpace(name) == "" {
		return release{}, errors.New("release name must be between 1 and 200 characters")
	}
	if len(body) > 50000 {
		return release{}, errors.New("release body is too long")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return release{}, err
	}
	commit, err := tagCommit(path, tag)
	if err != nil {
		return release{}, err
	}
	var created release
	err = s.updateReleases(func(db *releaseDB) error {
		for _, item := range db.Repos[repo] {
			if item.Tag == tag {
				return errors.New("a release already exists for this tag")
			}
		}
		id := 1
		for _, item := range db.Repos[repo] {
			if item.ID >= id {
				id = item.ID + 1
			}
		}
		now := time.Now().UTC()
		created = release{ID: id, Repo: repo, Tag: tag, Name: strings.TrimSpace(name), Body: body, Author: author, Draft: draft, Prerelease: prerelease, Commit: commit, CreatedAt: now, UpdatedAt: now}
		db.Repos[repo] = append(db.Repos[repo], created)
		return nil
	})
	return created, err
}

func (s *store) listReleases(repo string) ([]release, error) {
	db, err := s.loadReleases()
	if err != nil {
		return nil, err
	}
	items := make([]release, 0, len(db.Repos[repo]))
	items = append(items, db.Repos[repo]...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, nil
}

func (s *store) findRelease(repo string, id int) (release, error) {
	db, err := s.loadReleases()
	if err != nil {
		return release{}, err
	}
	for _, item := range db.Repos[repo] {
		if item.ID == id {
			return item, nil
		}
	}
	return release{}, errors.New("release not found")
}

func (s *store) deleteRelease(repo string, id int) error {
	err := s.updateReleases(func(db *releaseDB) error {
		items := db.Repos[repo]
		for i, item := range items {
			if item.ID == id {
				db.Repos[repo] = append(items[:i], items[i+1:]...)
				return nil
			}
		}
		return errors.New("release not found")
	})
	if err == nil {
		if parts := strings.SplitN(repo, "/", 2); len(parts) == 2 {
			_ = os.RemoveAll(filepath.Join(s.root, "release-assets", parts[0], parts[1], strconv.Itoa(id)))
		}
	}
	return err
}

func writeGitArchive(w io.Writer, repoPath, tag string) error {
	cmd := execCommand("git", "-C", repoPath, "archive", "--format=tar", "--prefix=release/", "refs/tags/"+tag)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(w, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	return waitErr
}

func writeGzipArchive(w io.Writer, repoPath, tag string) error {
	gz := gzip.NewWriter(w)
	if err := writeGitArchive(gz, repoPath, tag); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}
