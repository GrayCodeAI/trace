package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func (a *app) npmHTTP(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/npm/"), "/")
	if len(parts) < 3 || !namePattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) || !validPackagePart(parts[2]) {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	packageName := parts[2]
	if !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPut {
		if !u.canWrite(repo) || isArchived(mustRepoPath(a.store, repo)) || isMirror(mustRepoPath(a.store, repo)) {
			apiError(w, http.StatusForbidden, "write access required and repository must be writable")
			return
		}
		var input struct {
			Name     string `json:"name"`
			Versions map[string]struct {
				Version string `json:"version"`
			} `json:"versions"`
			Attachments map[string]struct {
				Data string `json:"data"`
			} `json:"_attachments"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxPackageSize+(2<<20))).Decode(&input); err != nil || input.Name != packageName || len(input.Versions) == 0 || len(input.Attachments) != 1 {
			apiError(w, http.StatusBadRequest, "unsupported npm publish payload")
			return
		}
		version := ""
		for key, item := range input.Versions {
			version = item.Version
			if version == "" {
				version = key
			}
			break
		}
		for filename, item := range input.Attachments {
			data, err := base64.StdEncoding.DecodeString(item.Data)
			if err != nil || int64(len(data)) > maxPackageSize {
				apiError(w, http.StatusBadRequest, "invalid npm attachment")
				return
			}
			artifact, err := a.store.publishPackage(repo, packageName, version, filepath.Base(filename), username, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": artifact.ID})
			return
		}
	}
	items, err := a.store.listPackages(repo)
	if err != nil {
		http.Error(w, "cannot list packages", http.StatusInternalServerError)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodGet {
		versions := map[string]map[string]any{}
		latest := ""
		for _, item := range items {
			if item.Name != packageName {
				continue
			}
			if latest == "" || item.Version > latest {
				latest = item.Version
			}
			versions[item.Version] = map[string]any{
				"name": item.Name, "version": item.Version,
				"dist": map[string]string{"tarball": "/npm/" + repo + "/" + item.Name + "/-/" + item.Filename, "shasum": item.SHA256},
			}
		}
		if len(versions) == 0 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": packageName, "dist-tags": map[string]string{"latest": latest}, "versions": versions})
		return
	}
	if len(parts) == 5 && parts[3] == "-" && r.Method == http.MethodGet {
		filename := filepath.Base(parts[4])
		for _, item := range items {
			if item.Name != packageName || item.Filename != filename {
				continue
			}
			_, path, err := a.store.packageArtifact(repo, item.Name, item.Version, item.Filename)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
			http.ServeFile(w, r, path)
			return
		}
	}
	_ = username
	http.NotFound(w, r)
}

func mustRepoPath(s *store, repo string) string {
	path, _ := s.repoPath(repo)
	return path
}
