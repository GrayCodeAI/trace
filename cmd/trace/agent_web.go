package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
)

type agentPageData struct {
	Name       string
	CSRF       string
	CanWrite   bool
	CanImport  bool
	CanPublish bool
	Public     bool
	Git        agentGitResult
	Message    string
	Sessions   []agentSession
}

//go:embed agent.html
var agentHTML string

var agentPageTemplate = template.Must(template.New("agents").Parse(strings.Replace(agentHTML, "<!-- TRACE_AGENT_STYLES -->", pageStyle+repoPageStyle, 1)))

func (a *app) agentPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/agents")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if !u.canWrite(name) {
			http.Error(w, "write access required", http.StatusForbidden)
			return
		}
		if !a.form(w, r) {
			return
		}
		if _, err := a.store.createAgentSession(name, r.PostForm.Get("agent"), r.PostForm.Get("ref"), r.PostForm.Get("summary"), username); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/repos/"+name+"/agents", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := a.store.listAgentSessions(name)
	if err != nil {
		http.Error(w, "cannot list agent sessions", http.StatusInternalServerError)
		return
	}
	gitStatus, err := a.store.agentGitStatus(name)
	if err != nil {
		http.Error(w, "cannot read Git agent history", http.StatusInternalServerError)
		return
	}
	path, err := a.store.repoPath(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	public := isPublic(path)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	message := ""
	if r.URL.Query().Get("imported") == "1" {
		message = "Signed agent bundle imported. Existing records were kept; new checkpoints were added."
	}
	if r.URL.Query().Get("published") == "1" {
		message = "Signed agent history published to the private Git ref."
	}
	if err := agentPageTemplate.Execute(w, agentPageData{Name: name, CSRF: a.csrfFor(username), CanWrite: u.canWrite(name) && !isMirror(path) && !isArchived(path), CanImport: u.Admin, CanPublish: u.Admin && !public && !isMirror(path) && !isArchived(path), Public: public, Git: gitStatus, Message: message, Sessions: sessions}); err != nil {
		log.Printf("render agent sessions: %v", err)
	}
}

func (a *app) agentGitPublishPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/agents/publish-git")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	result, err := a.store.publishAgentGitBundle(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if result.Changed {
		_ = a.store.recordAudit(username, "agent_git.publish", name, result.Ref, map[string]any{"commit": result.Commit, "sessions": result.Sessions})
	}
	http.Redirect(w, r, "/repos/"+name+"/agents?published=1", http.StatusSeeOther)
}

func (a *app) agentBundleImportPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/agents/import")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentBundleBytes+128<<10)
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		http.Error(w, "invalid bundle upload", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrfFor(username))) != 1 {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		http.Error(w, "signed JSON bundle is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxAgentBundleBytes+64<<10+1))
	if err != nil || len(encoded) > maxAgentBundleBytes+64<<10 {
		http.Error(w, "bundle exceeds size limit", http.StatusBadRequest)
		return
	}
	var bundle signedAgentBundle
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		http.Error(w, "invalid signed bundle JSON", http.StatusBadRequest)
		return
	}
	result, err := a.store.importAgentBundle(name, r.PostForm.Get("node_id"), bundle)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(username, "agent_bundle.import", name, r.PostForm.Get("node_id"), map[string]any{"sessions_created": result.SessionsCreated, "checkpoints_added": result.CheckpointsAdded})
	http.Redirect(w, r, "/repos/"+name+"/agents?imported=1", http.StatusSeeOther)
}
