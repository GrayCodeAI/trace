package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

type secretDB struct {
	Repos map[string]map[string]string `json:"repos"`
}

func (s *store) loadSecrets() (secretDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "secrets.json"))
	if errors.Is(err, os.ErrNotExist) {
		return secretDB{Repos: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return secretDB{}, err
	}
	var db secretDB
	if err := json.Unmarshal(b, &db); err != nil {
		return secretDB{}, fmt.Errorf("read secrets: %w", err)
	}
	if db.Repos == nil {
		db.Repos = map[string]map[string]string{}
	}
	return db, nil
}

func (s *store) updateSecrets(change func(*secretDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".secrets.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadSecrets()
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
	tmp, err := os.CreateTemp(s.root, ".secrets-")
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
	return os.Rename(name, filepath.Join(s.root, "secrets.json"))
}

func validateSecretName(name string) error {
	if !secretNamePattern.MatchString(name) || strings.HasPrefix(name, "TRACE_") {
		return errors.New("secret name must be 1-64 letters, digits, or underscores and cannot start with TRACE_")
	}
	return nil
}

func (s *store) setSecret(repo, name, value string) error {
	if !validRepoName(repo) {
		return errors.New("invalid repository")
	}
	if err := validateSecretName(name); err != nil {
		return err
	}
	if value == "" || len(value) > 64<<10 {
		return errors.New("secret value must be between 1 byte and 64 KiB")
	}
	if _, err := s.repoPath(repo); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.repos, strings.ReplaceAll(repo, "/", string(filepath.Separator))+".git")); err != nil {
		return errors.New("repository not found")
	}
	return s.updateSecrets(func(db *secretDB) error {
		if db.Repos[repo] == nil {
			db.Repos[repo] = map[string]string{}
		}
		db.Repos[repo][name] = value
		return nil
	})
}

func (s *store) deleteSecret(repo, name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	return s.updateSecrets(func(db *secretDB) error {
		values := db.Repos[repo]
		if _, ok := values[name]; !ok {
			return errors.New("secret not found")
		}
		delete(values, name)
		if len(values) == 0 {
			delete(db.Repos, repo)
		}
		return nil
	})
}

func (s *store) listSecretNames(repo string) ([]string, error) {
	db, err := s.loadSecrets()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(db.Repos[repo]))
	for name := range db.Repos[repo] {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func (s *store) actionSecrets(repo string) (map[string]string, error) {
	db, err := s.loadSecrets()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(db.Repos[repo]))
	for name, value := range db.Repos[repo] {
		result["TRACE_SECRET_"+name] = value
	}
	return result, nil
}

func (a *app) apiSecrets(w http.ResponseWriter, r *http.Request, u userRecord, repo string, tail []string) {
	if !u.Admin && roleFor(u, repo) != "write" {
		apiError(w, http.StatusForbidden, "write access required")
		return
	}
	if len(tail) == 0 && r.Method == http.MethodGet {
		names, err := a.store.listSecretNames(repo)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list secrets")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"names": names})
		return
	}
	if len(tail) != 1 {
		apiError(w, http.StatusNotFound, "secret not found")
		return
	}
	name := tail[0]
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.store.setSecret(repo, name, input.Value); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": name})
	case http.MethodDelete:
		if err := a.store.deleteSecret(repo, name); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
