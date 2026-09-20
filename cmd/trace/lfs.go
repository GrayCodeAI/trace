package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxLFSObjectSize int64 = 100 << 20

var lfsOIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type lfsObjectRequest struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type lfsBatchRequest struct {
	Operation string             `json:"operation"`
	Transfers []string           `json:"transfers,omitempty"`
	Objects   []lfsObjectRequest `json:"objects"`
}

type lfsAction struct {
	Href string `json:"href"`
}

type lfsBatchObject struct {
	OID     string               `json:"oid"`
	Size    int64                `json:"size"`
	Actions map[string]lfsAction `json:"actions,omitempty"`
	Error   *lfsObjectError      `json:"error,omitempty"`
}

type lfsObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsBatchResponse struct {
	Transfer string           `json:"transfer"`
	Objects  []lfsBatchObject `json:"objects"`
}

func validLFSObject(oid string, size int64) bool {
	return lfsOIDPattern.MatchString(oid) && size >= 0 && size <= maxLFSObjectSize
}

func (s *store) lfsObjectPath(oid string) (string, error) {
	if !lfsOIDPattern.MatchString(oid) {
		return "", errors.New("invalid LFS object id")
	}
	return filepath.Join(s.root, "lfs", strings.ToLower(oid)), nil
}

func lfsBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme != "http" && scheme != "https" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return (&url.URL{Scheme: scheme, Host: r.Host}).String()
}

func (a *app) lfs(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	rel := strings.TrimPrefix(r.URL.Path, "/lfs/")
	parts := strings.Split(rel, "/")
	if len(parts) < 5 || !strings.HasSuffix(parts[1], ".git") || parts[2] != "info" || parts[3] != "lfs" {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if !validRepoName(repo) || !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 5 && parts[4] == "objects" && r.Method == http.MethodPost {
		a.lfsBatch(w, r, username, u, repo)
		return
	}
	if len(parts) == 6 && parts[4] == "objects" && lfsOIDPattern.MatchString(parts[5]) {
		a.lfsObject(w, r, u, repo, parts[5])
		return
	}
	http.NotFound(w, r)
}

func (a *app) lfsBatch(w http.ResponseWriter, r *http.Request, username string, u userRecord, repo string) {
	var input lfsBatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeLFSJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid LFS batch request"})
		return
	}
	if input.Operation != "upload" && input.Operation != "download" {
		writeLFSJSON(w, http.StatusBadRequest, map[string]string{"message": "operation must be upload or download"})
		return
	}
	if len(input.Objects) > 100 {
		writeLFSJSON(w, http.StatusBadRequest, map[string]string{"message": "too many LFS objects"})
		return
	}
	if input.Operation == "upload" && !u.canWrite(repo) {
		writeLFSJSON(w, http.StatusForbidden, map[string]string{"message": "write access required"})
		return
	}
	response := lfsBatchResponse{Transfer: "basic", Objects: make([]lfsBatchObject, 0, len(input.Objects))}
	for _, object := range input.Objects {
		item := lfsBatchObject{OID: strings.ToLower(object.OID), Size: object.Size}
		if !validLFSObject(item.OID, item.Size) {
			item.Error = &lfsObjectError{Code: http.StatusBadRequest, Message: "invalid LFS object id or size"}
			response.Objects = append(response.Objects, item)
			continue
		}
		path, _ := a.store.lfsObjectPath(item.OID)
		_, statErr := os.Stat(path)
		href := lfsBaseURL(r) + "/lfs/" + repo + ".git/info/lfs/objects/" + item.OID
		if input.Operation == "upload" {
			if statErr == nil {
				response.Objects = append(response.Objects, item)
				continue
			}
			item.Actions = map[string]lfsAction{"upload": {Href: href}}
		} else if statErr != nil {
			item.Error = &lfsObjectError{Code: http.StatusNotFound, Message: "LFS object not found"}
		} else {
			item.Actions = map[string]lfsAction{"download": {Href: href}}
		}
		response.Objects = append(response.Objects, item)
	}
	_ = username
	writeLFSJSON(w, http.StatusOK, response)
}

func (a *app) lfsObject(w http.ResponseWriter, r *http.Request, u userRecord, repo, oid string) {
	path, err := a.store.lfsObjectPath(oid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !u.canWrite(repo) {
			writeLFSJSON(w, http.StatusForbidden, map[string]string{"message": "write access required"})
			return
		}
		if r.ContentLength > maxLFSObjectSize {
			writeLFSJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "LFS object is too large"})
			return
		}
		if err := a.store.writeLFSObject(path, oid, r.Body, r.ContentLength); err != nil {
			writeLFSJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			writeLFSJSON(w, http.StatusNotFound, map[string]string{"message": "LFS object not found"})
			return
		}
		if err != nil {
			http.Error(w, "cannot open LFS object", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			http.Error(w, "cannot stat LFS object", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeLFSJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

func (s *store) writeLFSObject(path, oid string, source io.Reader, expectedSize int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".object-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	count, err := io.CopyN(io.MultiWriter(tmp, hash), source, maxLFSObjectSize+1)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		tmp.Close()
		return err
	}
	if count > maxLFSObjectSize || (expectedSize >= 0 && count != expectedSize) {
		tmp.Close()
		return errors.New("LFS object size mismatch")
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(oid) {
		tmp.Close()
		return errors.New("LFS object hash mismatch")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeLFSJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
