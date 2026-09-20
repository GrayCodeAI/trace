package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type apiRepo struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	CloneURL string `json:"clone_url"`
	Mirror   bool   `json:"mirror"`
	Archived bool   `json:"archived"`
	Public   bool   `json:"public"`
}

type apiBranch struct {
	Name    string `json:"name"`
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
}

type apiCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (a *app) api(w http.ResponseWriter, r *http.Request, db userDB) {
	username, u, ok := a.webIdentity(r, db)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Trace API"`)
		apiError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if r.Method != http.MethodGet {
		// Cookie-authenticated writes must carry an explicit CSRF header. Basic
		// auth is intended for non-browser CLI/API clients and is already bound
		// to the user's token.
		if _, _, hasBasic := r.BasicAuth(); !hasBasic {
			if r.Header.Get("X-Trace-CSRF") == "" || r.Header.Get("X-Trace-CSRF") != a.csrfFor(username) {
				apiError(w, http.StatusForbidden, "missing or invalid X-Trace-CSRF header")
				return
			}
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if strings.HasPrefix(path, "/users") {
		a.apiUsers(w, r, u, username, strings.TrimPrefix(path, "/users"))
		return
	}
	if path == "/ssh/host-key" {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if r.Method == http.MethodGet {
			publicKey, fingerprint, err := sshHostKeyInfo(a.store.root)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot load SSH host key")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"public_key": strings.TrimSpace(publicKey), "fingerprint": fingerprint})
			return
		}
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		publicKey, fingerprint, err := rotateSSHHostKey(a.store.root)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot rotate SSH host key")
			return
		}
		_ = a.store.recordAudit(username, "ssh.host_key.rotate", "", "host-key", map[string]any{"fingerprint": fingerprint})
		writeJSON(w, http.StatusOK, map[string]string{"public_key": strings.TrimSpace(publicKey), "fingerprint": fingerprint, "restart_required": "true"})
		return
	}
	if strings.HasPrefix(path, "/federation/peers") {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if path == "/federation/peers" {
			switch r.Method {
			case http.MethodGet:
				peers, err := a.store.loadFederationPeers()
				if err != nil {
					apiError(w, http.StatusInternalServerError, "cannot load federation peers")
					return
				}
				writeJSON(w, http.StatusOK, peers)
				return
			case http.MethodPost:
				var peer federationPeer
				if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&peer); err != nil {
					apiError(w, http.StatusBadRequest, "invalid federation peer")
					return
				}
				if err := a.store.addFederationPeer(peer); err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
				writeJSON(w, http.StatusCreated, peer)
				return
			default:
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
		}
		if path == "/federation/peers/sync" {
			if r.Method != http.MethodPost {
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if err := a.store.syncFederationPeers(""); err != nil {
				apiError(w, http.StatusBadGateway, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
			return
		}
		if strings.HasPrefix(path, "/federation/peers/") && r.Method == http.MethodDelete {
			repo := strings.TrimPrefix(path, "/federation/peers/")
			if !validRepoName(repo) {
				apiError(w, http.StatusBadRequest, "invalid repository")
				return
			}
			if err := a.store.removeFederationPeer(repo); err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusNoContent, map[string]string{})
			return
		}
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if path == "/federation/conflicts" {
		if !u.Admin || r.Method != http.MethodGet {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		conflicts, err := a.store.loadFederationConflicts()
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot load federation conflicts")
			return
		}
		writeJSON(w, http.StatusOK, conflicts)
		return
	}
	if path == "/federation/conflicts/resolve" {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			Repo      string `json:"repo"`
			Source    string `json:"source"`
			Ref       string `json:"ref"`
			LocalSHA  string `json:"local_sha"`
			RemoteSHA string `json:"remote_sha"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid federation resolution")
			return
		}
		if err := a.store.approveFederationConflict(input.Repo, input.Source, input.Ref, input.LocalSHA, input.RemoteSHA, username); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "federation.conflict.approve_source", input.Repo, input.Ref, map[string]any{"source": input.Source, "local_sha": input.LocalSHA, "remote_sha": input.RemoteSHA})
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "next_step": "retry signed mirror sync"})
		return
	}
	if path == "/federation/trust" {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		switch r.Method {
		case http.MethodGet:
			pins, err := a.store.loadFederationTrust()
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot load federation trust")
				return
			}
			writeJSON(w, http.StatusOK, pins)
		case http.MethodDelete:
			var input struct {
				Repo           string `json:"repo"`
				Source         string `json:"source"`
				ExpectedNodeID string `json:"expected_node_id"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if err := a.store.forgetFederationIdentity(input.Repo, input.Source, input.ExpectedNodeID); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "federation.trust.forget", input.Repo, input.Source, nil)
			writeJSON(w, http.StatusNoContent, map[string]string{})
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if strings.HasPrefix(path, "/teams") {
		tail := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, "/teams"), "/"), "/")
		if len(tail) == 1 && tail[0] == "" {
			tail = nil
		}
		a.apiTeams(w, r, u, tail)
		return
	}
	if strings.HasPrefix(path, "/federation/repos/") && strings.HasSuffix(path, "/manifest") {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(path, "/federation/repos/"), "/manifest")
		if !validRepoName(repo) || !u.canRead(repo) {
			apiError(w, http.StatusNotFound, "not found")
			return
		}
		manifest, err := a.store.repositoryManifest(repo)
		if err != nil {
			apiError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, manifest)
		return
	}
	if path == "/audit" {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				apiError(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = parsed
		}
		events, err := a.store.listAudit(limit)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	if path == "/notifications" {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		unreadOnly := r.URL.Query().Get("unread") != "false"
		items, err := a.store.listNotifications(username, unreadOnly)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list notifications")
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if strings.HasPrefix(path, "/notifications/") && strings.HasSuffix(path, "/read") {
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		idText := strings.TrimSuffix(strings.TrimPrefix(path, "/notifications/"), "/read")
		id, err := strconv.Atoi(strings.Trim(idText, "/"))
		if err != nil || id < 1 {
			apiError(w, http.StatusNotFound, "notification not found")
			return
		}
		if err := a.store.markNotificationRead(username, id); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusNoContent, map[string]string{})
		return
	}
	if path == "/repos" {
		a.apiRepos(w, r, u)
		return
	}
	if path == "/search" {
		a.apiGlobalSearch(w, r, u)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" || !namePattern.MatchString(parts[1]) || !namePattern.MatchString(parts[2]) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	name := parts[1] + "/" + parts[2]
	if !validRepoName(name) || !u.canRead(name) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	repoPath, err := a.store.repoPath(name)
	if err != nil {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := os.Stat(repoPath); err != nil {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 4 && parts[3] == "stars" {
		users, err := a.store.listStars(name)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list stars")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"count": len(users), "starred": starContainsString(users, username), "users": users})
		case http.MethodPut, http.MethodPost:
			if err := a.store.setStar(name, username, true); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"count": len(users) + boolInt(!starContainsString(users, username)), "starred": true})
		case http.MethodDelete:
			if err := a.store.setStar(name, username, false); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusNoContent, map[string]string{})
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) == 4 && parts[3] == "watchers" {
		users, err := a.store.listWatches(name)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list watchers")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"count": len(users), "watching": starContainsString(users, username), "users": users})
		case http.MethodPut, http.MethodPost:
			if err := a.store.setWatch(name, username, true); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"count": len(users) + boolInt(!starContainsString(users, username)), "watching": true})
		case http.MethodDelete:
			if err := a.store.setWatch(name, username, false); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusNoContent, map[string]string{})
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) == 4 && parts[3] == "topics" {
		switch r.Method {
		case http.MethodGet:
			topics, err := a.store.getTopics(name)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list topics")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"topics": topics})
		case http.MethodPut, http.MethodPatch:
			if !u.canMaintain(name) {
				apiError(w, http.StatusForbidden, "maintain access required")
				return
			}
			var input struct {
				Topics []string `json:"topics"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "topics array is required")
				return
			}
			if err := a.store.setTopics(name, input.Topics); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			topics, _ := a.store.getTopics(name)
			_ = a.store.recordAudit(username, "repository.topics.update", name, "api", map[string]any{"topics": topics})
			writeJSON(w, http.StatusOK, map[string]any{"topics": topics})
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) >= 4 && parts[3] == "projects" {
		a.apiProjects(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) == 4 && parts[3] == "transfer" {
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil || !validRepoName(input.Name) {
			apiError(w, http.StatusBadRequest, "target name must be OWNER/NAME")
			return
		}
		if err := a.store.transferRepo(name, input.Name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, apiRepo{Name: input.Name, Role: "admin", CloneURL: "/git/" + input.Name + ".git"})
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if err := a.store.deleteRepo(name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 4 && (parts[3] == "archive" || parts[3] == "restore") {
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		archived := parts[3] == "archive"
		if err := a.store.setArchived(name, archived); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, apiRepo{Name: name, Role: roleFor(u, name), CloneURL: "/git/" + name + ".git", Mirror: isMirror(repoPath), Archived: archived, Public: isPublic(repoPath)})
		return
	}
	if len(parts) == 4 && parts[3] == "visibility" {
		if r.Method != http.MethodPost || !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		var input struct {
			Public *bool `json:"public"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil || input.Public == nil {
			apiError(w, http.StatusBadRequest, "public boolean is required")
			return
		}
		if err := a.store.setPublic(name, *input.Public); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, apiRepo{Name: name, Role: roleFor(u, name), CloneURL: "/git/" + name + ".git", Mirror: isMirror(repoPath), Archived: isArchived(repoPath), Public: *input.Public})
		return
	}
	if len(parts) == 4 && parts[3] == "fork" {
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			Name   string `json:"name"`
			Public bool   `json:"public"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil || !validRepoName(input.Name) {
			apiError(w, http.StatusBadRequest, "fork name must be OWNER/NAME")
			return
		}
		targetOwner := strings.SplitN(input.Name, "/", 2)[0]
		if !u.Admin && targetOwner != username {
			apiError(w, http.StatusForbidden, "fork target must belong to the authenticated user")
			return
		}
		if _, ok := db.Users[targetOwner]; !ok {
			apiError(w, http.StatusBadRequest, "fork target owner does not exist")
			return
		}
		if err := a.store.forkRepo(name, input.Name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.store.updateUsers(func(users *userDB) error {
			target := users.Users[targetOwner]
			if target.Repos == nil {
				target.Repos = map[string]string{}
			}
			target.Repos[input.Name] = "write"
			users.Users[targetOwner] = target
			return nil
		}); err != nil {
			apiError(w, http.StatusInternalServerError, "fork created but access grant failed")
			return
		}
		writeJSON(w, http.StatusCreated, apiRepo{Name: input.Name, Role: "write", CloneURL: "/git/" + input.Name + ".git"})
		return
	}
	if len(parts) == 3 {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, apiRepo{Name: name, Role: roleFor(u, name), CloneURL: "/git/" + name + ".git", Mirror: isMirror(repoPath), Archived: isArchived(repoPath), Public: isPublic(repoPath)})
		return
	}
	if len(parts) >= 4 && parts[3] == "pulls" {
		a.apiPullRequests(w, r, u, username, name, repoPath, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "merge-queue" {
		a.apiMergeQueue(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "issues" {
		a.apiIssues(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "webhooks" {
		a.apiWebhooks(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "releases" {
		a.apiReleases(w, r, u, username, name, repoPath, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "milestones" {
		a.apiMilestones(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) >= 4 && parts[3] == "packages" {
		a.apiPackages(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) == 4 && parts[3] == "search" {
		a.apiSearch(w, r, name, repoPath)
		return
	}
	if len(parts) == 4 && parts[3] == "search-index" {
		a.apiSearchIndex(w, r, u, name)
		return
	}
	if len(parts) == 4 && parts[3] == "policy" {
		a.apiPolicy(w, r, u, username, name)
		return
	}
	if len(parts) >= 5 && parts[3] == "actions" && parts[4] == "runs" {
		a.apiActions(w, r, u, username, name, parts[5:])
		return
	}
	if len(parts) >= 4 && parts[3] == "secrets" {
		a.apiSecrets(w, r, u, name, parts[4:])
		return
	}
	if len(parts) == 4 && parts[3] == "pages" {
		a.apiPages(w, r, u, name)
		return
	}
	if len(parts) == 4 && parts[3] == "raw" {
		a.apiRaw(w, r, name, repoPath)
		return
	}
	if len(parts) >= 4 && parts[3] == "agent-sessions" {
		a.apiAgentSessions(w, r, u, username, name, parts[4:])
		return
	}
	if len(parts) == 5 && parts[3] == "statuses" {
		commit := parts[4]
		if r.Method == http.MethodGet {
			statuses, err := a.store.listCommitStatuses(name, commit)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list commit statuses")
				return
			}
			writeJSON(w, http.StatusOK, statuses)
			return
		}
		if r.Method != http.MethodPost || !u.canWrite(name) || isArchived(repoPath) || isMirror(repoPath) {
			apiError(w, http.StatusForbidden, "write access required and repository must be writable")
			return
		}
		var status commitStatus
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&status); err != nil {
			apiError(w, http.StatusBadRequest, "invalid status JSON")
			return
		}
		status.Creator = username
		if err := a.store.setCommitStatus(name, commit, status); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, status)
		return
	}
	if len(parts) != 4 && !(len(parts) == 5 && parts[3] == "branches") {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[3] {
	case "branches":
		if r.Method == http.MethodGet && len(parts) == 4 {
			a.apiBranches(w, r, repoPath)
		} else {
			a.apiBranchMutation(w, r, u, name, repoPath, parts[4:])
		}
	case "commits":
		a.apiCommits(w, r, repoPath)
	default:
		apiError(w, http.StatusNotFound, "not found")
	}
}

func (a *app) apiMergeQueue(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if !u.canMaintain(repo) {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	if len(tail) == 0 {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		entries, err := a.store.listMergeQueue(repo)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot load merge queue")
			return
		}
		writeJSON(w, http.StatusOK, entries)
		return
	}
	if len(tail) != 1 || r.Method != http.MethodPost {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	switch tail[0] {
	case "enqueue":
		var input struct {
			PullRequestID int    `json:"pull_request_id"`
			Strategy      string `json:"strategy"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil || input.PullRequestID < 1 {
			apiError(w, http.StatusBadRequest, "pull_request_id is required")
			return
		}
		entry, err := a.store.enqueueMerge(repo, input.PullRequestID, input.Strategy, username)
		if err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "merge_queue.enqueue", repo, fmt.Sprint(entry.PullRequestID), map[string]any{"strategy": entry.Strategy})
		writeJSON(w, http.StatusCreated, entry)
	case "process":
		entry, err := a.store.processMergeQueue(repo, username)
		if err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "merge_queue.process", repo, fmt.Sprint(entry.PullRequestID), map[string]any{"state": entry.State})
		writeJSON(w, http.StatusOK, entry)
	default:
		apiError(w, http.StatusNotFound, "not found")
	}
}

// apiUsers exposes the same user lifecycle as the local dashboard and CLI.
// Tokens are returned only when they are created or rotated; stored records
// contain hashes and never expose credentials.
func (a *app) apiUsers(w http.ResponseWriter, r *http.Request, u userRecord, actor, tail string) {
	if !u.Admin {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	if tail == "" || tail == "/" {
		switch r.Method {
		case http.MethodGet:
			db, err := a.store.loadUsers()
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot load users")
				return
			}
			type userView struct {
				Name     string `json:"name"`
				Admin    bool   `json:"admin"`
				Disabled bool   `json:"disabled"`
			}
			items := make([]userView, 0, len(db.Users))
			for name, item := range db.Users {
				items = append(items, userView{Name: name, Admin: item.Admin, Disabled: item.Disabled})
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			var input struct {
				Name  string `json:"name"`
				Admin bool   `json:"admin"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			token, err := a.store.addUser(input.Name, input.Admin)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(actor, "user.create", "", input.Name, map[string]any{"admin": input.Admin})
			writeJSON(w, http.StatusCreated, map[string]any{"name": input.Name, "admin": input.Admin, "token": token})
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 2 && parts[1] == "rotate" && r.Method == http.MethodPost {
		token, err := a.store.rotateUser(parts[0])
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(actor, "user.rotate_token", "", parts[0], nil)
		writeJSON(w, http.StatusOK, map[string]any{"name": parts[0], "token": token})
		return
	}
	if len(parts) != 1 {
		apiError(w, http.StatusNotFound, "user not found")
		return
	}
	name := parts[0]
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			Disabled *bool `json:"disabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil || input.Disabled == nil {
			apiError(w, http.StatusBadRequest, "disabled is required")
			return
		}
		err := a.store.updateUsers(func(db *userDB) error {
			item, ok := db.Users[name]
			if !ok {
				return errors.New("user not found")
			}
			if item.Admin && name == actor && *input.Disabled {
				return errors.New("cannot disable the current administrator")
			}
			item.Disabled = *input.Disabled
			db.Users[name] = item
			return nil
		})
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(actor, "user.status.update", "", name, map[string]any{"disabled": *input.Disabled})
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "disabled": *input.Disabled})
	case http.MethodDelete:
		if err := a.store.removeUser(name); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(actor, "user.remove", "", name, nil)
		writeJSON(w, http.StatusNoContent, map[string]string{})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) apiPolicy(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string) {
	if !u.Admin {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		policy, err := a.store.repoPolicy(repo)
		if err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, policy)
	case http.MethodPatch:
		var input repoPolicy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		policy, err := a.store.setRepoPolicy(repo, input)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "repo.policy.update", repo, "policy", map[string]any{"required_approvals": policy.RequiredApprovals, "required_checks": policy.RequiredChecks, "protected_branches": policy.ProtectedBranches, "require_codeowners": policy.RequireCodeOwners})
		writeJSON(w, http.StatusOK, policy)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) apiWebhooks(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if !u.Admin {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := a.store.listWebhooks(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list webhooks")
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			var input struct {
				URL    string   `json:"url"`
				Secret string   `json:"secret"`
				Events []string `json:"events"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			hook, err := a.store.addWebhook(repo, input.URL, input.Secret, input.Events)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "webhook.create", repo, fmt.Sprint(hook.ID), map[string]any{"url": hook.URL})
			hook.Secret = ""
			writeJSON(w, http.StatusCreated, hook)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(tail) == 2 && tail[1] == "deliveries" && r.Method == http.MethodGet {
		id, err := strconv.Atoi(tail[0])
		if err != nil || id < 1 {
			apiError(w, http.StatusNotFound, "webhook not found")
			return
		}
		items, err := a.store.listWebhookDeliveries(repo, id)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot list webhook deliveries")
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if len(tail) != 1 || r.Method != http.MethodDelete {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err := a.store.removeWebhook(repo, id); err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	_ = a.store.recordAudit(username, "webhook.delete", repo, fmt.Sprint(id), nil)
	writeJSON(w, http.StatusNoContent, map[string]string{})
}

func (a *app) apiReleases(w http.ResponseWriter, r *http.Request, u userRecord, username, repo, repoPath string, tail []string) {
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := a.store.listReleases(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list releases")
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			if !u.canWrite(repo) {
				apiError(w, http.StatusForbidden, "write access required")
				return
			}
			var input struct {
				Tag        string `json:"tag"`
				Name       string `json:"name"`
				Body       string `json:"body"`
				Draft      bool   `json:"draft"`
				Prerelease bool   `json:"prerelease"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			item, err := a.store.createRelease(repo, input.Tag, input.Name, input.Body, username, input.Draft, input.Prerelease)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "release.create", repo, fmt.Sprint(item.ID), map[string]any{"tag": item.Tag})
			notifyWatchers(a.store, repo, username, "release.created", fmt.Sprint(item.ID), username+" published "+item.Tag)
			writeJSON(w, http.StatusCreated, item)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(tail) == 2 && tail[0] == "archive" {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tag := tail[1]
		if _, err := tagCommit(repoPath, tag); err != nil {
			apiError(w, http.StatusNotFound, "tag not found")
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeDownloadName(repo)+"-"+safeDownloadName(tag)+`.tar.gz"`)
		w.Header().Set("Cache-Control", "public, max-age=60")
		if err := writeGzipArchive(w, repoPath, tag); err != nil {
			return
		}
		return
	}
	if len(tail) >= 2 && tail[1] == "assets" {
		id, err := strconv.Atoi(tail[0])
		if err != nil || id < 1 {
			apiError(w, http.StatusNotFound, "release not found")
			return
		}
		if len(tail) == 2 {
			switch r.Method {
			case http.MethodGet:
				assets, err := a.store.listReleaseAssets(repo, id)
				if err != nil {
					apiError(w, http.StatusNotFound, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, assets)
			case http.MethodPost:
				if !u.canWrite(repo) || isArchived(repoPath) || isMirror(repoPath) {
					apiError(w, http.StatusForbidden, "write access required and repository must be writable")
					return
				}
				name := strings.TrimSpace(r.Header.Get("X-Asset-Name"))
				if name == "" {
					name = strings.TrimSpace(r.URL.Query().Get("name"))
				}
				if !validReleaseAssetName(name) {
					apiError(w, http.StatusBadRequest, "X-Asset-Name must be a path-safe filename")
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, maxReleaseAssetSize+1)
				asset, err := a.store.addReleaseAsset(repo, id, name, r.Header.Get("Content-Type"), r.Body)
				if err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = a.store.recordAudit(username, "release.asset.create", repo, fmt.Sprint(id), map[string]any{"name": asset.Name, "size": asset.Size})
				writeJSON(w, http.StatusCreated, asset)
			default:
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		if len(tail) == 3 && r.Method == http.MethodGet {
			name := tail[2]
			path, err := a.store.releaseAssetPath(repo, id, name)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			assets, err := a.store.listReleaseAssets(repo, id)
			if err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			var found *releaseAsset
			for i := range assets {
				if assets[i].Name == name {
					found = &assets[i]
					break
				}
			}
			if found == nil {
				apiError(w, http.StatusNotFound, "release asset not found")
				return
			}
			if _, err := os.Stat(path); err != nil {
				apiError(w, http.StatusNotFound, "release asset file not found")
				return
			}
			if found.ContentType != "" {
				w.Header().Set("Content-Type", found.ContentType)
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+safeDownloadName(name)+`"`)
			http.ServeFile(w, r, path)
			return
		}
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if len(tail) != 1 {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "release not found")
		return
	}
	if r.Method == http.MethodGet {
		item, err := a.store.findRelease(repo, id)
		if err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		if err := a.store.deleteRelease(repo, id); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "release.delete", repo, fmt.Sprint(id), nil)
		writeJSON(w, http.StatusNoContent, map[string]string{})
		return
	}
	apiError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *app) apiMilestones(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := a.store.listMilestones(repo, r.URL.Query().Get("state"))
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list milestones")
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			if !u.canWrite(repo) {
				apiError(w, http.StatusForbidden, "write access required")
				return
			}
			var input struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				DueDate     string `json:"due_date"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			item, err := a.store.createMilestone(repo, input.Title, input.Description, input.DueDate)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "milestone.create", repo, fmt.Sprint(item.ID), nil)
			writeJSON(w, http.StatusCreated, item)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(tail) != 1 {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "milestone not found")
		return
	}
	if r.Method == http.MethodGet {
		items, err := a.store.listMilestones(repo, "all")
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot load milestones")
			return
		}
		for _, item := range items {
			if item.ID == id {
				writeJSON(w, http.StatusOK, item)
				return
			}
		}
		apiError(w, http.StatusNotFound, "milestone not found")
		return
	}
	if !u.Admin || (r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	if r.Method == http.MethodDelete {
		if err := a.store.deleteMilestone(repo, id); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "milestone.delete", repo, fmt.Sprint(id), nil)
		writeJSON(w, http.StatusNoContent, map[string]string{})
		return
	}
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		DueDate     string `json:"due_date"`
		State       string `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	item, err := a.store.updateMilestone(repo, id, input.Title, input.Description, input.State, input.DueDate)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = a.store.recordAudit(username, "milestone.update", repo, fmt.Sprint(id), nil)
	writeJSON(w, http.StatusOK, item)
}

func safeDownloadName(raw string) string {
	raw = strings.ReplaceAll(raw, "/", "-")
	raw = strings.ReplaceAll(raw, "\\", "-")
	if raw == "" {
		return "release"
	}
	return raw
}

func (a *app) apiIssues(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			state := r.URL.Query().Get("state")
			if state != "" && state != "open" && state != "closed" && state != "all" {
				apiError(w, http.StatusBadRequest, "state must be open, closed, or all")
				return
			}
			items, err := a.store.listIssues(repo, state)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list issues")
				return
			}
			writeJSON(w, http.StatusOK, items)
			return
		}
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Title    string   `json:"title"`
			Body     string   `json:"body"`
			Assignee string   `json:"assignee"`
			Labels   []string `json:"labels"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		item, err := a.store.createIssue(repo, input.Title, input.Body, username, input.Assignee, input.Labels)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "issue.create", repo, fmt.Sprint(item.ID), map[string]any{"labels": item.Labels})
		notifyUsers(a.store, []string{item.Assignee}, "issue.assigned", repo, fmt.Sprint(item.ID), "You were assigned an issue: "+item.Title)
		notifyWatchers(a.store, repo, username, "issue.created", fmt.Sprint(item.ID), username+" opened issue #"+fmt.Sprint(item.ID))
		writeJSON(w, http.StatusCreated, item)
		return
	}
	if len(tail) == 2 {
		id, err := strconv.Atoi(tail[0])
		if err != nil || id < 1 {
			apiError(w, http.StatusNotFound, "issue not found")
			return
		}
		if tail[1] != "comments" || r.Method != http.MethodPost {
			apiError(w, http.StatusNotFound, "not found")
			return
		}
		var input struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		item, err := a.store.addIssueComment(repo, id, username, input.Body)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "issue.comment", repo, fmt.Sprint(id), nil)
		notifyUsers(a.store, []string{item.Author, item.Assignee}, "issue.commented", repo, fmt.Sprint(id), username+" commented on issue #"+fmt.Sprint(id))
		notifyWatchers(a.store, repo, username, "issue.commented", fmt.Sprint(id), username+" commented on issue #"+fmt.Sprint(id))
		writeJSON(w, http.StatusOK, item)
		return
	}
	if len(tail) != 1 {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "issue not found")
		return
	}
	db, err := a.store.loadIssues()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot load issues")
		return
	}
	item, err := findIssue(&db, repo, id)
	if err != nil {
		apiError(w, http.StatusNotFound, "issue not found")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Assignee  string   `json:"assignee"`
		State     string   `json:"state"`
		Labels    []string `json:"labels"`
		Milestone string   `json:"milestone"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&input); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updated, err := a.store.updateIssue(repo, id, username, u.Admin, input.Title, input.Body, input.Assignee, input.State, input.Labels)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.Milestone != "" {
		updated, err = a.store.setIssueMilestone(repo, id, username, u.Admin, input.Milestone)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	_ = a.store.recordAudit(username, "issue.update", repo, fmt.Sprint(id), map[string]any{"state": updated.State})
	notifyUsers(a.store, []string{updated.Author, updated.Assignee}, "issue.updated", repo, fmt.Sprint(id), username+" updated issue #"+fmt.Sprint(id))
	notifyWatchers(a.store, repo, username, "issue.updated", fmt.Sprint(id), username+" updated issue #"+fmt.Sprint(id))
	writeJSON(w, http.StatusOK, updated)
}

func (a *app) apiPullRequests(w http.ResponseWriter, r *http.Request, u userRecord, username, repo, repoPath string, tail []string) {
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			state := r.URL.Query().Get("state")
			if state != "" && state != "open" && state != "closed" && state != "merged" && state != "all" {
				apiError(w, http.StatusBadRequest, "state must be open, closed, merged, or all")
				return
			}
			prs, err := a.store.listPullRequests(repo, state)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list pull requests")
				return
			}
			writeJSON(w, http.StatusOK, prs)
			return
		}
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Source      string   `json:"source"`
			Target      string   `json:"target"`
			Draft       bool     `json:"draft"`
			Labels      []string `json:"labels"`
			Assignees   []string `json:"assignees"`
			Reviewers   []string `json:"reviewers"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		pr, err := a.store.createPullRequestWithDraft(repo, input.Title, input.Description, username, input.Source, input.Target, input.Draft)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(input.Labels) > 0 || len(input.Assignees) > 0 {
			pr, err = a.store.updatePullMetadata(repo, pr.ID, input.Labels, input.Assignees)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if len(input.Reviewers) > 0 {
			pr, err = a.store.updatePullReviewers(repo, pr.ID, input.Reviewers)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			notifyUsers(a.store, input.Reviewers, "pull_request.review_requested", repo, fmt.Sprint(pr.ID), username+" requested your review on pull request #"+fmt.Sprint(pr.ID))
		}
		_ = a.store.recordAudit(username, "pull_request.create", repo, fmt.Sprint(pr.ID), map[string]any{"source": pr.Source, "target": pr.Target})
		owner := strings.SplitN(repo, "/", 2)[0]
		notifyUsers(a.store, []string{owner}, "pull_request.created", repo, fmt.Sprint(pr.ID), username+" opened pull request #"+fmt.Sprint(pr.ID))
		notifyWatchers(a.store, repo, username, "pull_request.created", fmt.Sprint(pr.ID), username+" opened pull request #"+fmt.Sprint(pr.ID))
		writeJSON(w, http.StatusCreated, pr)
		return
	}
	if len(tail) == 2 {
		id, err := strconv.Atoi(tail[0])
		if err != nil || id < 1 {
			apiError(w, http.StatusNotFound, "pull request not found")
			return
		}
		switch tail[1] {
		case "comments":
			if r.Method != http.MethodPost {
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			var input struct {
				Body string `json:"body"`
				File string `json:"file"`
				Line int    `json:"line"`
				Side string `json:"side"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			pr, err := a.store.addPullCommentAt(repo, id, username, input.Body, input.File, input.Line, input.Side)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "pull_request.comment", repo, fmt.Sprint(id), nil)
			notifyUsers(a.store, []string{pr.Author}, "pull_request.commented", repo, fmt.Sprint(id), username+" commented on pull request #"+fmt.Sprint(id))
			notifyWatchers(a.store, repo, username, "pull_request.commented", fmt.Sprint(id), username+" commented on pull request #"+fmt.Sprint(id))
			writeJSON(w, http.StatusOK, pr)
		case "approve":
			if r.Method != http.MethodPost {
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			pr, err := a.store.approvePullRequest(repo, id, username)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "pull_request.approve", repo, fmt.Sprint(id), nil)
			notifyUsers(a.store, []string{pr.Author}, "pull_request.approved", repo, fmt.Sprint(id), username+" approved pull request #"+fmt.Sprint(id))
			notifyWatchers(a.store, repo, username, "pull_request.approved", fmt.Sprint(id), username+" approved pull request #"+fmt.Sprint(id))
			writeJSON(w, http.StatusOK, pr)
		case "merge":
			if r.Method != http.MethodPost || !u.canMaintain(repo) {
				apiError(w, http.StatusForbidden, "maintain access required to merge")
				return
			}
			var input struct {
				Strategy string `json:"strategy"`
			}
			if r.Body != nil {
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil && err != io.EOF {
					apiError(w, http.StatusBadRequest, "invalid JSON body")
					return
				}
			}
			pr, err := a.store.mergePullRequestWithStrategy(repo, id, username, input.Strategy)
			if err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "pull_request.merge", repo, fmt.Sprint(id), nil)
			notifyUsers(a.store, []string{pr.Author}, "pull_request.merged", repo, fmt.Sprint(id), username+" merged pull request #"+fmt.Sprint(id))
			notifyWatchers(a.store, repo, username, "pull_request.merged", fmt.Sprint(id), username+" merged pull request #"+fmt.Sprint(id))
			writeJSON(w, http.StatusOK, pr)
		case "ready":
			if r.Method != http.MethodPost {
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			pr, err := a.store.markPullRequestReady(repo, id, username, u.Admin)
			if err != nil {
				apiError(w, http.StatusConflict, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "pull_request.ready", repo, fmt.Sprint(id), nil)
			writeJSON(w, http.StatusOK, pr)
		case "metadata":
			if r.Method != http.MethodPatch || !u.canWrite(repo) {
				apiError(w, http.StatusForbidden, "write access required")
				return
			}
			var input struct {
				Labels    []string `json:"labels"`
				Assignees []string `json:"assignees"`
				Reviewers []string `json:"reviewers"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			pr, err := a.store.getPullRequest(repo, id)
			if err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			if input.Labels != nil || input.Assignees != nil {
				pr, err = a.store.updatePullMetadata(repo, id, input.Labels, input.Assignees)
				if err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
			if input.Reviewers != nil {
				pr, err = a.store.updatePullReviewers(repo, id, input.Reviewers)
				if err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
				notifyUsers(a.store, input.Reviewers, "pull_request.review_requested", repo, fmt.Sprint(id), username+" requested your review on pull request #"+fmt.Sprint(id))
			}
			_ = a.store.recordAudit(username, "pull_request.metadata", repo, fmt.Sprint(id), map[string]any{"labels": pr.Labels, "assignees": pr.Assignees})
			writeJSON(w, http.StatusOK, pr)
		case "close":
			if r.Method != http.MethodPost {
				apiError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			updated, err := a.store.closePullRequest(repo, id, username, u.Admin)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "pull_request.close", repo, fmt.Sprint(id), nil)
			writeJSON(w, http.StatusOK, updated)
		default:
			apiError(w, http.StatusNotFound, "not found")
		}
		return
	}
	if len(tail) != 1 {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "pull request not found")
		return
	}
	db, err := a.store.loadPullRequests()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot load pull requests")
		return
	}
	pr, err := findPullRequest(&db, repo, id)
	if err != nil {
		apiError(w, http.StatusNotFound, "pull request not found")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, pr)
		return
	}
	apiError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func roleFor(u userRecord, name string) string {
	if u.Admin {
		return "admin"
	}
	return u.Repos[name]
}

func (a *app) apiRepos(w http.ResponseWriter, r *http.Request, u userRecord) {
	if r.Method == http.MethodPost {
		if !u.Admin {
			apiError(w, http.StatusForbidden, "admin required")
			return
		}
		var input struct {
			Name   string `json:"name"`
			Public bool   `json:"public"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.store.createRepo(input.Name, false); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if input.Public {
			if err := a.store.setPublic(input.Name, true); err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		_ = a.store.recordAudit("admin", "repo.create", input.Name, "repository", nil)
		writeJSON(w, http.StatusCreated, map[string]any{"name": input.Name, "clone_url": "/git/" + input.Name + ".git", "public": input.Public})
		return
	}
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	owners, err := os.ReadDir(a.store.repos)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot list repositories")
		return
	}
	result := make([]apiRepo, 0)
	for _, owner := range owners {
		if !owner.IsDir() || !namePattern.MatchString(owner.Name()) {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(a.store.repos, owner.Name()))
		for _, entry := range entries {
			base := strings.TrimSuffix(entry.Name(), ".git")
			name := owner.Name() + "/" + base
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") || !namePattern.MatchString(base) || !u.canRead(name) {
				continue
			}
			repoPath := filepath.Join(a.store.repos, owner.Name(), entry.Name())
			result = append(result, apiRepo{Name: name, Role: roleFor(u, name), CloneURL: "/git/" + name + ".git", Mirror: isMirror(repoPath), Archived: isArchived(repoPath), Public: isPublic(repoPath)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	writeJSON(w, http.StatusOK, result)
}

func (a *app) apiBranches(w http.ResponseWriter, r *http.Request, repoPath string) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	refs, _, err := gitOutput(repoPath, 1<<20, "for-each-ref", "--format=%(refname:short)\t%(objectname:short)\t%(subject)", "refs/heads")
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot list branches")
		return
	}
	branches := make([]apiBranch, 0)
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) == 3 && fields[0] != "" {
			branches = append(branches, apiBranch{Name: fields[0], Commit: fields[1], Subject: fields[2]})
		}
	}
	writeJSON(w, http.StatusOK, branches)
}

func (a *app) apiBranchMutation(w http.ResponseWriter, r *http.Request, u userRecord, repo, repoPath string, tail []string) {
	if !u.canWrite(repo) || isMirror(repoPath) || isArchived(repoPath) {
		apiError(w, http.StatusForbidden, "write access required and repository must be writable")
		return
	}
	if len(tail) == 0 && r.Method == http.MethodPost {
		var input struct {
			Name string `json:"name"`
			From string `json:"from"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.store.createBranch(repo, input.Name, input.From); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": input.Name, "from": input.From})
		return
	}
	if len(tail) == 1 && r.Method == http.MethodDelete {
		if err := a.store.deleteBranch(repo, tail[0]); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusNoContent, map[string]string{})
		return
	}
	apiError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *app) apiCommits(w http.ResponseWriter, r *http.Request, repoPath string) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			apiError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	logText, _, err := gitOutput(repoPath, 1<<20, "log", "--all", "-n", strconv.Itoa(limit), "--format=%H\t%an\t%s")
	if err != nil {
		apiError(w, http.StatusInternalServerError, "cannot list commits")
		return
	}
	commits := make([]apiCommit, 0)
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) == 3 && fields[0] != "" {
			commits = append(commits, apiCommit{Hash: fields[0], Author: fields[1], Subject: fields[2]})
		}
	}
	writeJSON(w, http.StatusOK, commits)
}
