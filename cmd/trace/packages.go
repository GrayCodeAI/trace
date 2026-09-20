package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

func packageCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace package <publish|list|get> [-data DIR] OWNER/NAME NAME VERSION FILE [OUTPUT]")
	}
	fs := flag.NewFlagSet("package "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "publish":
		if fs.NArg() != 5 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace package publish [-data DIR] OWNER/NAME NAME VERSION FILENAME SOURCE_FILE")
		}
		file, err := os.Open(fs.Arg(4))
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		artifact, err := s.publishPackage(fs.Arg(0), fs.Arg(1), fs.Arg(2), fs.Arg(3), "local-cli", file, info.Size())
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(artifact, "", "  ")
		fmt.Println(string(b))
		return nil
	case "list":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace package list [-data DIR] OWNER/NAME")
		}
		items, err := s.listPackages(fs.Arg(0))
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(b))
		return nil
	case "get":
		if fs.NArg() != 5 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace package get [-data DIR] OWNER/NAME NAME VERSION FILENAME OUTPUT")
		}
		_, path, err := s.packageArtifact(fs.Arg(0), fs.Arg(1), fs.Arg(2), fs.Arg(3))
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(fs.Arg(4), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return errors.New("usage: trace package <publish|list|get> [-data DIR] ...")
	}
}

const maxPackageSize int64 = 100 << 20

var packagePartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type packageArtifact struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Publisher string    `json:"publisher"`
	CreatedAt time.Time `json:"created_at"`
}

type packageDB struct {
	Artifacts []packageArtifact `json:"artifacts"`
}

func validPackagePart(value string) bool { return packagePartPattern.MatchString(value) }

func (s *store) loadPackages() (packageDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "packages.json"))
	if errors.Is(err, os.ErrNotExist) {
		return packageDB{Artifacts: []packageArtifact{}}, nil
	}
	if err != nil {
		return packageDB{}, err
	}
	var db packageDB
	if err := json.Unmarshal(b, &db); err != nil {
		return packageDB{}, fmt.Errorf("read packages: %w", err)
	}
	if db.Artifacts == nil {
		db.Artifacts = []packageArtifact{}
	}
	return db, nil
}

func (s *store) updatePackages(change func(*packageDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".packages.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadPackages()
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
	tmp, err := os.CreateTemp(s.root, ".packages-*")
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
	return os.Rename(name, filepath.Join(s.root, "packages.json"))
}

func packageID(repo, name, version, filename string) string {
	return repo + "/" + name + "/" + version + "/" + filename
}

func (s *store) packagePath(repo, name, version, filename string) (string, error) {
	if _, err := s.repoPath(repo); err != nil {
		return "", err
	}
	if !validPackagePart(name) || !validPackagePart(version) || !validPackagePart(filename) {
		return "", errors.New("invalid package name, version, or filename")
	}
	return filepath.Join(s.root, "packages", strings.Split(repo, "/")[0], strings.Split(repo, "/")[1], name, version, filename), nil
}

func (s *store) publishPackage(repo, name, version, filename, publisher string, source io.Reader, size int64) (packageArtifact, error) {
	path, err := s.packagePath(repo, name, version, filename)
	if err != nil {
		return packageArtifact{}, err
	}
	if size > maxPackageSize {
		return packageArtifact{}, errors.New("package is too large")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return packageArtifact{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".package-*")
	if err != nil {
		return packageArtifact{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	count, err := io.CopyN(io.MultiWriter(tmp, hash), source, maxPackageSize+1)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		tmp.Close()
		return packageArtifact{}, err
	}
	if count > maxPackageSize || (size >= 0 && count != size) {
		tmp.Close()
		return packageArtifact{}, errors.New("package size mismatch")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return packageArtifact{}, err
	}
	if err := tmp.Close(); err != nil {
		return packageArtifact{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	artifact := packageArtifact{ID: packageID(repo, name, version, filename), Repo: repo, Name: name, Version: version, Filename: filename, Size: count, SHA256: digest, Publisher: publisher, CreatedAt: time.Now().UTC()}
	err = s.updatePackages(func(db *packageDB) error {
		for _, existing := range db.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("package artifact already exists")
			}
		}
		return nil
	})
	if err != nil {
		return packageArtifact{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return packageArtifact{}, err
	}
	if err := s.updatePackages(func(db *packageDB) error { db.Artifacts = append(db.Artifacts, artifact); return nil }); err != nil {
		return packageArtifact{}, err
	}
	return artifact, nil
}

func (s *store) listPackages(repo string) ([]packageArtifact, error) {
	db, err := s.loadPackages()
	if err != nil {
		return nil, err
	}
	items := make([]packageArtifact, 0)
	for _, artifact := range db.Artifacts {
		if artifact.Repo == repo {
			items = append(items, artifact)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *store) packageArtifact(repo, name, version, filename string) (packageArtifact, string, error) {
	id := packageID(repo, name, version, filename)
	db, err := s.loadPackages()
	if err != nil {
		return packageArtifact{}, "", err
	}
	for _, artifact := range db.Artifacts {
		if artifact.ID == id {
			path, err := s.packagePath(repo, name, version, filename)
			return artifact, path, err
		}
	}
	return packageArtifact{}, "", errors.New("package artifact not found")
}

func (a *app) packageHTTP(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/packages/"), "/")
	if len(parts) < 4 || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if !validRepoName(repo) || !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	name, version, filename := parts[2], parts[3], ""
	if len(parts) == 5 {
		filename = parts[4]
	}
	if r.Method == http.MethodPut {
		if !u.canWrite(repo) || filename == "" {
			http.Error(w, "write access required", http.StatusForbidden)
			return
		}
		artifact, err := a.store.publishPackage(repo, name, version, filename, username, r.Body, r.ContentLength)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, artifact)
		return
	}
	if r.Method != http.MethodGet || filename == "" {
		http.NotFound(w, r)
		return
	}
	artifact, path, err := a.store.packageArtifact(repo, name, version, filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Trace-Package-SHA256", artifact.SHA256)
	http.ServeContent(w, r, artifact.Filename, artifact.CreatedAt, file)
}

func (a *app) apiPackages(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 0 && r.Method == http.MethodGet {
		items, err := a.store.listPackages(repo)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list packages")
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if len(tail) != 3 || r.Method != http.MethodPut || !u.canWrite(repo) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	artifact, err := a.store.publishPackage(repo, tail[0], tail[1], tail[2], username, r.Body, r.ContentLength)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}
