package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const pageStyle = `<style>
@font-face{font-family:Inter;src:url("/assets/inter.woff2") format("woff2");font-style:normal;font-weight:100 900;font-display:swap}
:root{color-scheme:dark;--bg:#07151a;--panel:#0d2427;--line:#1c4c50;--text:#e6fbf8;--muted:#99c8c7;--lime:#38d5c5;--accent:#22b8ad}html[data-theme="light"]{color-scheme:light;--bg:#f2fbfa;--panel:#ffffff;--line:#b8dcd8;--text:#0a2d31;--muted:#527678;--lime:#008f87;--accent:#007a73}html[data-theme="light"] pre{background:#e4f3f1;color:#123b3d}html[data-theme="light"] input,html[data-theme="light"] select{background:#fff;color:var(--text);border-color:#8ec6c1}html[data-theme="light"] button{background:#d9f1ee;color:#0a3d3e;border-color:#7cbcb7}html[data-theme="light"] button:hover{background:#bde5e0}
*{box-sizing:border-box}body{font:15px/1.6 Inter,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;max-width:1000px;margin:0 auto;padding:2rem 1.5rem 5rem;background:var(--bg);color:var(--text)}
a{color:var(--lime)}.theme-toggle{position:fixed;right:18px;bottom:18px;z-index:20;border:1px solid var(--line);border-radius:999px;background:var(--panel);color:var(--text);box-shadow:0 8px 24px rgba(0,40,45,.22);padding:.55rem .8rem;font-size:12px}.theme-toggle:hover{background:var(--accent);color:#fff}a:focus-visible,button:focus-visible,input:focus-visible,select:focus-visible{outline:2px solid var(--lime);outline-offset:3px}h1{font-size:36px;letter-spacing:-.055em;margin:0 0 .4rem}h2{font-size:23px;letter-spacing:-.04em;margin:0 0 1rem}h3{margin:1.5rem 0 .7rem}p{color:var(--muted)}li{margin:.65rem 0}ul{padding-left:1.5rem}section{border:1px solid var(--line);background:var(--panel);border-radius:14px;padding:1.5rem;margin-top:1.5rem}section section{border:0;padding:0}pre{background:#0a130c;border:1px solid var(--line);padding:1rem;overflow:auto;white-space:pre-wrap;border-radius:9px;color:#d9e8d5;font:13px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace}code{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--lime)}input,select{font:inherit;padding:.55rem;margin:.3rem;background:#0b140e;border:1px solid #56705a;border-radius:7px;color:var(--text);max-width:100%}button{font:700 13px Inter,system-ui,sans-serif;cursor:pointer;padding:.65rem .85rem;margin:.3rem;border:1px solid #698069;border-radius:8px;background:#243425;color:var(--text)}button:hover{background:#37503a}form{margin:.8rem 0}.muted{color:var(--muted)}.secret{background:#24371c;color:#e5ffd2;border:1px solid var(--lime);padding:1rem;border-radius:10px}.topline{display:flex;align-items:center;justify-content:space-between;gap:1rem;flex-wrap:wrap;border-bottom:1px solid var(--line);padding-bottom:1rem}.topline p{margin:0}.topline form{margin:0}.topline h1{display:flex;align-items:center;gap:10px}.topline h1 img,.repo-nav img{width:38px;height:38px;display:block;image-rendering:auto;background:#fff;border-radius:8px;padding:3px}.repo-nav{display:flex;align-items:center;justify-content:space-between;gap:1rem;margin-bottom:1.4rem}.repo-nav a:first-child{display:inline-flex;align-items:center;gap:10px;color:var(--text);font-size:20px;font-weight:750;text-decoration:none;letter-spacing:-.045em}label{color:var(--text)}@media(max-width:620px){body{padding:1.2rem 1rem 3rem}section{padding:1rem}input{width:100%}}
</style><script>(function(){var k="trace-theme",root=document.documentElement;var saved=localStorage.getItem(k);if(saved)root.dataset.theme=saved;document.addEventListener("DOMContentLoaded",function(){var b=document.createElement("button");b.className="theme-toggle";b.type="button";b.setAttribute("aria-label","Toggle color theme");function paint(){var light=root.dataset.theme==="light";b.textContent=light?"☾ Dark":"☀ Light";b.setAttribute("aria-pressed",String(light))}b.addEventListener("click",function(){root.dataset.theme=root.dataset.theme==="light"?"dark":"light";localStorage.setItem(k,root.dataset.theme);paint()});document.body.appendChild(b);paint()})})();</script>`

type repoLink struct {
	Name string
	Role string
	URL  string
}

type userView struct {
	Name   string
	Role   string
	Access string
}

type indexData struct {
	Username string
	Admin    bool
	CSRF     string
	Repos    []repoLink
	Users    []userView
	TokenFor string
	NewToken string
}

type federationPageData struct {
	Username  string
	CSRF      string
	Peers     []federationPeer
	Trust     []federationTrust
	Conflicts []federationConflictPageView
	Message   string
}

type federationConflictPageView struct {
	federationConflict
	Approved bool
}

var federationPageTemplate = template.Must(template.New("federation").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Federation · Trace</title>` + pageStyle + federationPageStyle + `</head><body><div class="topline"><h1><img src="/assets/trace-mark.svg?v=4" alt=""> Trace federation</h1><p><a href="/app">Back to workspace</a></p></div><p>Signed peer replication for administrators.</p>{{if .Message}}<section><strong>{{.Message}}</strong></section>{{end}}<section><h2>Configured peers</h2>{{if .Peers}}<ul>{{range .Peers}}<li><code>{{.Repo}}</code> ← {{.Source}} ({{.Username}}) <form method="post" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="remove"><input type="hidden" name="repo" value="{{.Repo}}"><button>Remove</button></form></li>{{end}}</ul>{{else}}<p>No peers configured.</p>{{end}}{{if .Peers}}<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="sync"><button>Sync all peers</button></form>{{end}}</section><section><h2>Add or replace a peer</h2><form class="federation-form" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="add"><label>Repository <input name="repo" placeholder="owner/name" required></label><label>Source Git URL <input name="source" placeholder="https://trace.example/git/owner/name.git" required></label><label>Source username <input name="username" value="admin" required></label><label>Token file path on this server <input name="token_file" placeholder="/etc/trace/source-token" required></label><button>Add peer</button></form></section><section><h2>Replication conflicts</h2><p class="muted">A conflicting ref remains on the mirror until an administrator approves the exact signed source object ID and retries sync. Approving a source deletion removes that mirror ref on the next successful sync.</p>{{range .Conflicts}}<article class="conflict-record"><h3>{{.Repo}} · <code>{{.Ref}}</code></h3><p class="muted">Source: <code>{{.Source}}</code></p><dl><div><dt>Mirror</dt><dd><code>{{.LocalSHA}}</code></dd></div><div><dt>Signed source</dt><dd><code>{{if .RemoteSHA}}{{.RemoteSHA}}{{else}}deleted{{end}}</code></dd></div></dl>{{if .Approved}}<p class="conflict-approved">Approved by {{.ApprovedBy}}. Retry signed peer sync to apply.</p>{{else}}<form method="post" onsubmit="return confirm('Accept the signed source for this exact conflict? The next sync may replace or delete the mirror ref.')"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="resolve"><input type="hidden" name="repo" value="{{.Repo}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="ref" value="{{.Ref}}"><input type="hidden" name="local_sha" value="{{.LocalSHA}}"><input type="hidden" name="remote_sha" value="{{.RemoteSHA}}"><button type="submit">Accept signed source</button></form>{{end}}</article>{{else}}<p class="muted">No unresolved conflicts.</p>{{end}}</section><section><h2>Pinned source identities</h2><p class="muted">Trace pins each source node ID at first verified sync and rejects a changed identity. Verify any replacement ID through a trusted channel before forgetting a pin.</p>{{range .Trust}}<article class="trust-record"><h3>{{.Repo}}</h3><p><code>{{.Source}}</code></p><p style="overflow-wrap:anywhere">Node ID: <code>{{.NodeID}}</code></p><form method="post" onsubmit="return confirm('Forget this pinned source identity? The next signed sync will trust its current key.')"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="forget-trust"><input type="hidden" name="repo" value="{{.Repo}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="node_id" value="{{.NodeID}}"><button type="submit">Forget pin</button></form></article>{{else}}<p class="muted">No source identities pinned yet.</p>{{end}}</section></body></html>`))

func (a *app) federationPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	message := ""
	if r.Method == http.MethodPost {
		if !a.form(w, r) {
			return
		}
		switch r.PostForm.Get("action") {
		case "add":
			err := a.store.addFederationPeer(federationPeer{Repo: r.PostForm.Get("repo"), Source: r.PostForm.Get("source"), Username: r.PostForm.Get("username"), TokenFile: r.PostForm.Get("token_file")})
			if err != nil {
				message = err.Error()
			} else {
				message = "Peer saved."
			}
		case "remove":
			if err := a.store.removeFederationPeer(r.PostForm.Get("repo")); err != nil {
				message = err.Error()
			} else {
				message = "Peer removed."
			}
		case "sync":
			if err := a.store.syncFederationPeers(""); err != nil {
				message = err.Error()
			} else {
				message = "Peer sync completed."
			}
		case "forget-trust":
			if err := a.store.forgetFederationIdentity(r.PostForm.Get("repo"), r.PostForm.Get("source"), r.PostForm.Get("node_id")); err != nil {
				message = err.Error()
			} else {
				message = "Source identity forgotten. Verify the new node ID before the next sync."
				_ = a.store.recordAudit(username, "federation.trust.forget", r.PostForm.Get("repo"), r.PostForm.Get("source"), nil)
			}
		case "resolve":
			if err := a.store.approveFederationConflict(r.PostForm.Get("repo"), r.PostForm.Get("source"), r.PostForm.Get("ref"), r.PostForm.Get("local_sha"), r.PostForm.Get("remote_sha"), username); err != nil {
				message = err.Error()
			} else {
				message = "Signed source approved for this exact conflict. Retry peer sync to apply it."
				_ = a.store.recordAudit(username, "federation.conflict.approve_source", r.PostForm.Get("repo"), r.PostForm.Get("ref"), map[string]any{"source": r.PostForm.Get("source"), "local_sha": r.PostForm.Get("local_sha"), "remote_sha": r.PostForm.Get("remote_sha")})
			}
		}
	}
	peers, err := a.store.loadFederationPeers()
	if err != nil {
		http.Error(w, "cannot load federation peers", http.StatusInternalServerError)
		return
	}
	trust, err := a.store.loadFederationTrust()
	if err != nil {
		http.Error(w, "cannot load federation trust", http.StatusInternalServerError)
		return
	}
	allConflicts, err := a.store.loadFederationConflicts()
	if err != nil {
		http.Error(w, "cannot load federation conflicts", http.StatusInternalServerError)
		return
	}
	var conflicts []federationConflictPageView
	for _, conflict := range allConflicts {
		if conflict.AppliedAt.IsZero() && conflict.SupersededAt.IsZero() {
			conflicts = append(conflicts, federationConflictPageView{federationConflict: conflict, Approved: !conflict.ApprovedAt.IsZero()})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = federationPageTemplate.Execute(w, federationPageData{Username: username, CSRF: a.csrfFor(username), Peers: peers, Trust: trust, Conflicts: conflicts, Message: message})
}

//go:embed dashboard.html
var dashboardHTML string

var indexTemplate = template.Must(template.New("index").Parse(dashboardHTML))

func (a *app) index(w http.ResponseWriter, username string, u userRecord, db userDB) {
	a.renderIndex(w, username, u, db, "", "")
}

func (a *app) csrfFor(username string) string {
	h := hmac.New(sha256.New, []byte(a.csrf))
	_, _ = h.Write([]byte(username))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (a *app) renderIndex(w http.ResponseWriter, username string, u userRecord, db userDB, tokenFor, newToken string) {
	owners, err := os.ReadDir(a.store.repos)
	if err != nil {
		http.Error(w, "cannot list repositories", http.StatusInternalServerError)
		return
	}
	data := indexData{Username: username, Admin: u.Admin, CSRF: a.csrfFor(username), TokenFor: tokenFor, NewToken: newToken}
	for _, owner := range owners {
		if !owner.IsDir() || !namePattern.MatchString(owner.Name()) {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(a.store.repos, owner.Name()))
		if err != nil {
			continue
		}
		for _, repo := range repos {
			base := strings.TrimSuffix(repo.Name(), ".git")
			name := owner.Name() + "/" + base
			if !repo.IsDir() || !strings.HasSuffix(repo.Name(), ".git") || !namePattern.MatchString(base) || !u.canRead(name) {
				continue
			}
			role := u.Repos[name]
			if u.Admin {
				role = "admin"
			}
			data.Repos = append(data.Repos, repoLink{Name: name, Role: role, URL: "/repos/" + name})
		}
	}
	sort.Slice(data.Repos, func(i, j int) bool { return data.Repos[i].Name < data.Repos[j].Name })
	if u.Admin {
		for name, member := range db.Users {
			role := "member"
			if member.Admin {
				role = "admin"
			}
			var access []string
			for repo, permission := range member.Repos {
				access = append(access, repo+": "+permission)
			}
			sort.Strings(access)
			if len(access) == 0 {
				access = []string{"no grants"}
			}
			data.Users = append(data.Users, userView{Name: name, Role: role, Access: strings.Join(access, ", ")})
		}
		sort.Slice(data.Users, func(i, j int) bool { return data.Users[i].Name < data.Users[j].Name })
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := indexTemplate.Execute(w, data); err != nil {
		log.Printf("render index: %v", err)
	}
}

func (a *app) form(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return false
	}
	username, _, ok := a.webIdentity(r, db)
	if !ok {
		http.Error(w, "authentication required", http.StatusForbidden)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrfFor(username))) != 1 {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return false
	}
	return true
}

func (a *app) requestActor(r *http.Request) string {
	db, err := a.store.loadUsers()
	if err != nil {
		return "unknown"
	}
	name, _, ok := a.webIdentity(r, db)
	if !ok {
		return "unknown"
	}
	return name
}

func (a *app) create(w http.ResponseWriter, r *http.Request, u userRecord) {
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	if err := a.store.createRepo(r.PostForm.Get("name"), false); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(a.requestActor(r), "repo.create", r.PostForm.Get("name"), "repository", nil)
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (a *app) createUser(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	token, err := a.store.addUser(name, r.PostForm.Get("admin") == "1")
	if err == nil {
		_ = a.store.recordAudit(a.requestActor(r), "user.create", "", name, map[string]any{"admin": r.PostForm.Get("admin") == "1"})
	}
	a.userChangeResult(w, r, name, token, err)
}

func (a *app) rotateUser(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	token, err := a.store.rotateUser(name)
	if err == nil {
		_ = a.store.recordAudit(a.requestActor(r), "user.rotate_token", "", name, nil)
	}
	a.userChangeResult(w, r, name, token, err)
}

func (a *app) userChangeResult(w http.ResponseWriter, r *http.Request, name, token string, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	username, u, ok := a.webIdentity(r, db)
	if !ok {
		http.Error(w, "authentication required", http.StatusForbidden)
		return
	}
	a.renderIndex(w, username, u, db, name, token)
}

func (a *app) removeUser(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) {
		return
	}
	if err := a.store.removeUser(r.PostForm.Get("name")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(a.requestActor(r), "user.remove", "", r.PostForm.Get("name"), nil)
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (a *app) grantAccess(w http.ResponseWriter, r *http.Request) {
	if !a.form(w, r) {
		return
	}
	if err := a.store.grantUser(r.PostForm.Get("user"), r.PostForm.Get("repo"), r.PostForm.Get("role")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(a.requestActor(r), "access.update", r.PostForm.Get("repo"), r.PostForm.Get("user"), map[string]any{"role": r.PostForm.Get("role")})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

type branchView struct {
	Name    string
	Commit  string
	Subject string
	URL     string
}

type fileView struct {
	Name string
	URL  string
}

type repoData struct {
	Name           string
	Branch         string
	Branches       []branchView
	Files          []fileView
	File           string
	Content        string
	RecentCommits  []commitSearchResult
	Diff           string
	FilesTruncated bool
	DiffTruncated  bool
	HasMain        bool
	IsWritable     bool
	CanWrite       bool
	IsAdmin        bool
	CanMaintain    bool
	Archived       bool
	CSRF           string
	PullRequests   []pullRequestView
	Issues         []issueView
	Statuses       []commitStatus
	Starred        bool
	StarCount      int
	Watching       bool
	WatchCount     int
	Topics         []string
}

// actionPageRuns is how many recent runs the actions page renders.
const actionPageRuns = 20

type actionPageData struct {
	Name     string
	CSRF     string
	CanWrite bool
	Secrets  []string
	Runs     []actionRun
}

type releasePageData struct {
	Name     string
	CSRF     string
	CanWrite bool
	Releases []release
	Error    string
}

var releasePageTemplate = template.Must(template.New("releases").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Releases · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Releases</h1>{{if .Error}}<p class="muted">{{.Error}}</p>{{end}}{{if .CanWrite}}<section><h2>Create release</h2><form action="/repos/{{.Name}}/releases" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><label>Tag <input name="tag" placeholder="v1.0.0" required maxlength="200"></label> <label>Title <input name="title" required maxlength="200"></label></p><p><label>Notes <textarea name="body" maxlength="50000" rows="5"></textarea></label></p><p><label><input type="checkbox" name="draft"> Draft</label> <label><input type="checkbox" name="prerelease"> Pre-release</label></p><button type="submit">Create release</button></form></section>{{end}}<section><h2>Published releases</h2>{{range .Releases}}<article><h3>{{.Name}} <span class="muted">({{.Tag}})</span></h3><p class="muted">{{if .Draft}}Draft{{else if .Prerelease}}Pre-release{{else}}Published{{end}} · {{.CreatedAt}} · by {{.Author}}</p>{{if .Body}}<pre>{{.Body}}</pre>{{end}}<p><a href="/api/v1/repos/{{$.Name}}/releases/archive/{{.Tag}}">Download source archive</a></p><h4>Assets</h4>{{if .Assets}}<ul>{{range .Assets}}<li><a href="/repos/{{$.Name}}/releases/{{.ID}}/assets/{{.Name}}">{{.Name}}</a> <span class="muted">({{.Size}} bytes, {{.SHA256}})</span></li>{{end}}</ul>{{else}}<p class="muted">No assets yet.</p>{{end}}{{if $.CanWrite}}<form action="/repos/{{$.Name}}/releases/{{.ID}}/assets" method="post" enctype="multipart/form-data"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>Upload asset <input type="file" name="asset" required></label><button type="submit">Upload</button></form>{{end}}</article>{{else}}<p class="muted">No releases yet.</p>{{end}}</section></html>`))

type searchPageData struct {
	Name              string
	CSRF              string
	Query             string
	Ref               string
	Scope             string
	Results           []searchResult
	Commits           []commitSearchResult
	Sessions          []agentSearchResult
	Truncated         bool
	CommitsTruncated  bool
	SessionsTruncated bool
	Error             string
}

type pullRequestView struct {
	ID        int
	Title     string
	Author    string
	Source    string
	Target    string
	State     string
	Draft     bool
	Comments  int
	Approvals int
	Reviewers string
}

type issueView struct {
	ID       int
	Title    string
	Author   string
	Assignee string
	Labels   string
	State    string
	Comments int
}

type pullPageData struct {
	Name        string
	CSRF        string
	User        string
	Admin       bool
	CanMaintain bool
	Pull        pullRequest
}

var pullPageTemplate = template.Must(template.New("pull").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Pull request #{{.Pull.ID}} · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>#{{.Pull.ID}} · {{.Pull.Title}}{{if .Pull.Draft}} <span class="muted">(draft)</span>{{end}}</h1><p class="muted">{{.Pull.Source}} → {{.Pull.Target}} · {{.Pull.State}} · opened by {{.Pull.Author}}{{if .Pull.Reviewers}} · reviewers: {{range .Pull.Reviewers}}{{.}} {{end}}{{end}}</p>{{if .Pull.Description}}<section><h2>Description</h2><pre>{{.Pull.Description}}</pre></section>{{end}}<section><h2>Review</h2>{{range .Pull.Comments}}<article><p class="muted"><strong>{{.Author}}</strong> · {{.CreatedAt}}{{if .Path}} · <code>{{.Path}}:{{.Line}}</code>{{end}}</p><pre>{{.Body}}</pre></article>{{else}}<p class="muted">No review comments yet.</p>{{end}}{{if eq .Pull.State "open"}}<form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/comment" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><label>Comment <textarea name="body" maxlength="20000" rows="4" required></textarea></label></p><p><label>File (optional) <input name="file" placeholder="src/example.go"></label> <label>Line <input name="line" type="number" min="1" placeholder="12"></label><input type="hidden" name="side" value="new"></p><button type="submit">Add review comment</button></form>{{end}}</section>{{if eq .Pull.State "open"}}{{if .CanMaintain}}<section><h2>Reviewers</h2><form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/reviewers" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Usernames <input name="reviewers" placeholder="alice,bob"></label><button type="submit">Request review</button></form></section>{{end}}<section><h2>Decision</h2>{{if .Pull.Draft}}{{if or .Admin (eq .Pull.Author .User)}}<form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/ready" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Mark ready for review</button></form>{{end}}{{else}}<form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/approve" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Approve</button></form>{{if .CanMaintain}}<form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/merge" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><select name="strategy"><option value="ff">Fast-forward</option><option value="squash">Squash</option><option value="merge">Merge commit</option></select><button type="submit">Merge now</button></form><form action="/repos/{{.Name}}/pulls/{{.Pull.ID}}/queue" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><select name="strategy"><option value="ff">Fast-forward</option><option value="squash">Squash</option><option value="merge">Merge commit</option></select><button type="submit">Add to merge queue</button></form>{{end}}{{end}}</section>{{end}}</html>`))

//go:embed repo.html
var repoHTML string

var repoTemplate = template.Must(template.New("repo").Parse(strings.Replace(repoHTML, "<!-- TRACE_REPO_STYLES -->", pageStyle+repoPageStyle, 1)))

var actionPageTemplate = template.Must(template.New("actions").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Actions · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Actions</h1><p class="muted">Runs execute on this Trace node from <code>.trace/workflow.json</code>. Secrets are exposed to jobs as <code>TRACE_SECRET_NAME</code> and never displayed here.</p>{{if .CanWrite}}<section><h2>CI secrets</h2><p class="muted">{{if .Secrets}}{{range .Secrets}}<code>{{.}}</code> {{end}}{{else}}No secrets configured.{{end}}</p><form action="/repos/{{.Name}}/secrets" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="set"><label>Name <input name="name" pattern="[A-Za-z_][A-Za-z0-9_]{0,63}" required></label><label>Value <input name="value" type="password" required></label><button type="submit">Save secret</button></form></section><section><h2>Run workflow</h2><form action="/repos/{{.Name}}/actions" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Branch <input name="ref" value="main" required></label><button type="submit">Run now</button></form></section>{{end}}<section><h2>Recent runs</h2>{{range $run := .Runs}}<article><h3>#{{$run.ID}} · {{$run.Status}}</h3><p class="muted">{{$run.Ref}} · {{$run.Commit}} · {{$run.TriggeredBy}} · {{$run.CreatedAt}}</p>{{range $run.Jobs}}<p><strong>{{.Name}}</strong>: {{.Status}} (exit {{.ExitCode}})</p>{{if .Log}}<pre>{{.Log}}</pre>{{end}}{{range .Artifacts}}<a href="/api/v1/repos/{{$.Name}}/actions/runs/{{$run.ID}}/artifacts/{{.Name}}">Download {{.Name}}</a>{{end}}{{end}}</article>{{else}}<p class="muted">No runs yet.</p>{{end}}</section></html>`))

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return n, nil
	}
	if n > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.truncated = true
		return n, nil
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func gitOutput(repo string, limit int, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var out cappedBuffer
	out.limit = limit
	var stderr cappedBuffer
	stderr.limit = 4096
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", false, fmt.Errorf("git %s: %s", args[0], message)
	}
	return out.String(), out.truncated, nil
}

func (a *app) repoPage(w http.ResponseWriter, r *http.Request, u userRecord, username string) {
	name := strings.TrimPrefix(r.URL.Path, "/repos/")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	path, _ := a.store.repoPath(name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	refs, truncated, err := gitOutput(path, 1<<20, "for-each-ref", "--format=%(refname:short)%09%(objectname:short)%09%(subject)", "refs/heads")
	if err != nil || truncated {
		http.Error(w, "cannot list branches", http.StatusInternalServerError)
		return
	}
	data := repoData{Name: name, IsWritable: u.canWrite(name) && !isMirror(path) && !isArchived(path), CanWrite: u.canWrite(name) && !isMirror(path) && !isArchived(path), IsAdmin: u.Admin, CanMaintain: u.canMaintain(name), Archived: isArchived(path), CSRF: a.csrfFor(username)}
	data.Topics, _ = a.store.getTopics(name)
	if stars, starErr := a.store.listStars(name); starErr == nil {
		data.StarCount = len(stars)
		data.Starred = starContainsString(stars, username)
	}
	if watchers, watchErr := a.store.listWatches(name); watchErr == nil {
		data.WatchCount = len(watchers)
		data.Watching = starContainsString(watchers, username)
	}
	prs, err := a.store.listPullRequests(name, "all")
	if err != nil {
		http.Error(w, "cannot list pull requests", http.StatusInternalServerError)
		return
	}
	for _, pr := range prs {
		data.PullRequests = append(data.PullRequests, pullRequestView{ID: pr.ID, Title: pr.Title, Author: pr.Author, Source: pr.Source, Target: pr.Target, State: pr.State, Draft: pr.Draft, Comments: len(pr.Comments), Approvals: len(pr.Approvals), Reviewers: strings.Join(pr.Reviewers, ", ")})
	}
	issues, err := a.store.listIssues(name, "all")
	if err != nil {
		http.Error(w, "cannot list issues", http.StatusInternalServerError)
		return
	}
	for _, item := range issues {
		data.Issues = append(data.Issues, issueView{ID: item.ID, Title: item.Title, Author: item.Author, Assignee: item.Assignee, Labels: strings.Join(item.Labels, ", "), State: item.State, Comments: len(item.Comments)})
	}
	branchNames := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		branchNames[fields[0]] = true
		data.Branches = append(data.Branches, branchView{Name: fields[0], Commit: fields[1], Subject: fields[2], URL: "/repos/" + name + "?branch=" + url.QueryEscape(fields[0])})
	}
	data.HasMain = branchNames["main"]
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
		if !data.HasMain && len(data.Branches) > 0 {
			branch = data.Branches[0].Name
		}
	}
	if len(data.Branches) > 0 && !branchNames[branch] {
		http.NotFound(w, r)
		return
	}
	if branchNames[branch] {
		data.Branch = branch
		ref := "refs/heads/" + branch
		filesRaw, filesTruncated, err := gitOutput(path, 1<<20, "ls-tree", "-r", "-z", "--name-only", ref)
		if err != nil {
			http.Error(w, "cannot list files", http.StatusInternalServerError)
			return
		}
		data.FilesTruncated = filesTruncated
		fileNames := make(map[string]bool)
		for _, file := range strings.Split(filesRaw, "\x00") {
			if file == "" || len(data.Files) >= 200 {
				continue
			}
			fileNames[file] = true
			data.Files = append(data.Files, fileView{Name: file, URL: "/repos/" + name + "?branch=" + url.QueryEscape(branch) + "&file=" + url.QueryEscape(file)})
		}
		if len(data.Files) >= 200 {
			data.FilesTruncated = true
		}
		if file := r.URL.Query().Get("file"); file != "" {
			if !fileNames[file] {
				http.NotFound(w, r)
				return
			}
			content, large, err := gitOutput(path, 128<<10, "show", ref+":"+file)
			if err != nil {
				http.Error(w, "cannot read file", http.StatusInternalServerError)
				return
			}
			data.File = file
			if large {
				data.Content = "File is too large for the web preview. Clone the repository to read it."
			} else if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
				data.Content = "Binary file. Clone the repository to read it."
			} else {
				data.Content = content
			}
		}
		data.RecentCommits = recentCommitLinks(path, branch)
		if commit, _, commitErr := gitOutput(path, 100, "rev-parse", ref); commitErr == nil {
			data.Statuses, _ = a.store.listCommitStatuses(name, strings.TrimSpace(commit))
		}
		if data.HasMain && branch != "main" {
			data.Diff, data.DiffTruncated, err = gitOutput(path, 256<<10, "diff", "--no-ext-diff", "--no-color", "refs/heads/main..."+ref, "--")
			if err != nil {
				http.Error(w, "cannot show branch changes", http.StatusInternalServerError)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := repoTemplate.Execute(w, data); err != nil {
		log.Printf("render repository: %v", err)
	}
}

func (a *app) searchPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/search")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	data := searchPageData{Name: name, CSRF: a.csrfFor(username), Query: r.URL.Query().Get("q"), Ref: r.URL.Query().Get("ref"), Scope: r.URL.Query().Get("scope")}
	if data.Ref == "" {
		data.Ref = "main"
	}
	if data.Scope == "" {
		data.Scope = "all"
	}
	if data.Query != "" {
		result, err := a.store.searchContext(name, data.Ref, data.Query, data.Scope)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Results, data.Commits, data.Sessions = result.Results, result.Commits, result.Sessions
			data.Truncated, data.CommitsTruncated, data.SessionsTruncated = result.Truncated, result.CommitsTruncated, result.SessionsTruncated
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := contextSearchPageTemplate.Execute(w, data); err != nil {
		log.Printf("render search: %v", err)
	}
}

func (a *app) pullPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[2] != "pulls" || !validRepoName(parts[0]+"/"+parts[1]) || !u.canRead(parts[0]+"/"+parts[1]) {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	db, err := a.store.loadPullRequests()
	if err != nil {
		http.Error(w, "cannot load pull request", http.StatusInternalServerError)
		return
	}
	pr, err := findPullRequest(&db, repo, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := pullPageTemplate.Execute(w, pullPageData{Name: repo, CSRF: a.csrfFor(username), User: username, Admin: u.Admin, CanMaintain: u.canMaintain(repo), Pull: *pr}); err != nil {
		log.Printf("render pull request: %v", err)
	}
}

func (a *app) actionPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/actions")
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
		run, err := a.store.actionRun(name, r.PostForm.Get("ref"), username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "action.run", name, strconv.Itoa(run.ID), map[string]any{"ref": run.Ref, "commit": run.Commit})
		http.Redirect(w, r, "/repos/"+name+"/actions", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs, err := a.store.listActionRuns(name)
	if err != nil {
		http.Error(w, "cannot list action runs", http.StatusInternalServerError)
		return
	}
	// Show the most recent runs with their logs; older runs remain available
	// through GET /api/v1/repos/OWNER/NAME/actions/runs/ID.
	if len(runs) > actionPageRuns {
		runs = runs[:actionPageRuns]
	}
	for i := range runs {
		a.store.hydrateActionLogs(&runs[i])
	}
	var secrets []string
	if u.canWrite(name) {
		secrets, err = a.store.listSecretNames(name)
		if err != nil {
			http.Error(w, "cannot list secrets", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := actionPageTemplate.Execute(w, actionPageData{Name: name, CSRF: a.csrfFor(username), CanWrite: u.canWrite(name), Secrets: secrets, Runs: runs}); err != nil {
		log.Printf("render actions: %v", err)
	}
}

func (a *app) releasePage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if !u.canWrite(name) || !a.form(w, r) {
			if !u.canWrite(name) {
				http.Error(w, "write access required", http.StatusForbidden)
			}
			return
		}
		item, err := a.store.createRelease(name, r.PostForm.Get("tag"), r.PostForm.Get("title"), r.PostForm.Get("body"), username, r.PostForm.Get("draft") == "on", r.PostForm.Get("prerelease") == "on")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "release.create", name, strconv.Itoa(item.ID), map[string]any{"tag": item.Tag})
		notifyWatchers(a.store, name, username, "release.created", strconv.Itoa(item.ID), username+" published "+item.Tag)
		http.Redirect(w, r, "/repos/"+name+"/releases", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := a.store.listReleases(name)
	if err != nil {
		http.Error(w, "cannot list releases", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := releasePageTemplate.Execute(w, releasePageData{Name: name, CSRF: a.csrfFor(username), CanWrite: u.canWrite(name), Releases: items}); err != nil {
		log.Printf("render releases: %v", err)
	}
}

func (a *app) releaseAssetPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[2] != "releases" || parts[4] != "assets" || !validRepoName(parts[0]+"/"+parts[1]) || !u.canWrite(parts[0]+"/"+parts[1]) {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id < 1 {
		http.Error(w, "invalid release", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseAssetSize+1<<20)
	if err := r.ParseMultipartForm(maxReleaseAssetSize + 1<<20); err != nil {
		http.Error(w, "asset upload is too large or invalid", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("asset")
	if err != nil {
		http.Error(w, "asset file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	asset, err := a.store.addReleaseAsset(parts[0]+"/"+parts[1], id, filepath.Base(header.Filename), header.Header.Get("Content-Type"), file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(username, "release.asset.create", parts[0]+"/"+parts[1], strconv.Itoa(id), map[string]any{"name": asset.Name, "size": asset.Size})
	http.Redirect(w, r, "/repos/"+parts[0]+"/"+parts[1]+"/releases", http.StatusSeeOther)
}

func (a *app) releaseAssetPageDownload(w http.ResponseWriter, r *http.Request, u userRecord) {
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[4] != "assets" || !validRepoName(parts[0]+"/"+parts[1]) || !u.canRead(parts[0]+"/"+parts[1]) {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id < 1 || !validReleaseAssetName(parts[5]) {
		http.NotFound(w, r)
		return
	}
	assets, err := a.store.listReleaseAssets(parts[0]+"/"+parts[1], id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var asset *releaseAsset
	for i := range assets {
		if assets[i].Name == parts[5] {
			asset = &assets[i]
			break
		}
	}
	if asset == nil {
		http.NotFound(w, r)
		return
	}
	filePath, err := a.store.releaseAssetPath(parts[0]+"/"+parts[1], id, asset.Name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if asset.ContentType != "" {
		w.Header().Set("Content-Type", asset.ContentType)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeDownloadName(asset.Name)+`"`)
	http.ServeFile(w, r, filePath)
}

func (a *app) starPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/star")
	if !validRepoName(repo) || !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	if !a.form(w, r) {
		return
	}
	starred := r.PostForm.Get("action") != "unstar"
	if err := a.store.setStar(repo, username, starred); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
}

func (a *app) watchPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/watch")
	if !validRepoName(repo) || !u.canRead(repo) {
		http.NotFound(w, r)
		return
	}
	if !a.form(w, r) {
		return
	}
	if err := a.store.setWatch(repo, username, r.PostForm.Get("action") != "unwatch"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
}

func (a *app) secretPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/secrets")
	if !validRepoName(repo) || !u.canWrite(repo) {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	var err error
	if r.PostForm.Get("action") == "delete" {
		err = a.store.deleteSecret(repo, name)
	} else {
		err = a.store.setSecret(repo, name, r.PostForm.Get("value"))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/repos/"+repo+"/actions", http.StatusSeeOther)
}

func (a *app) forkPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/fork")
	if !validRepoName(repo) || !u.canRead(repo) || !a.form(w, r) {
		if !validRepoName(repo) || !u.canRead(repo) {
			http.NotFound(w, r)
		}
		return
	}
	target := r.PostForm.Get("target")
	if !validRepoName(target) {
		http.Error(w, "fork name must be OWNER/NAME", http.StatusBadRequest)
		return
	}
	targetOwner := strings.SplitN(target, "/", 2)[0]
	if !u.Admin && targetOwner != username {
		http.Error(w, "fork target must belong to the authenticated user", http.StatusForbidden)
		return
	}
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	if _, ok := db.Users[targetOwner]; !ok {
		http.Error(w, "fork target owner does not exist", http.StatusBadRequest)
		return
	}
	if err := a.store.forkRepo(repo, target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.store.updateUsers(func(users *userDB) error {
		targetUser := users.Users[targetOwner]
		if targetUser.Repos == nil {
			targetUser.Repos = map[string]string{}
		}
		targetUser.Repos[target] = "write"
		users.Users[targetOwner] = targetUser
		return nil
	}); err != nil {
		http.Error(w, "fork created but access grant failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/repos/"+target, http.StatusSeeOther)
}

func (a *app) archivePageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	if !u.Admin || !a.form(w, r) {
		if !u.Admin {
			http.Error(w, "admin required", http.StatusForbidden)
		}
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) != 3 || !validRepoName(parts[0]+"/"+parts[1]) || (parts[2] != "archive" && parts[2] != "restore") {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if err := a.store.setArchived(repo, parts[2] == "archive"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
}

func (a *app) repoLifecycleAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) != 3 || !validRepoName(parts[0]+"/"+parts[1]) || (parts[2] != "delete" && parts[2] != "transfer") {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if parts[2] == "delete" {
		if err := a.store.deleteRepo(repo); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "repo.delete", repo, "repository", nil)
		http.Redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	target := r.PostForm.Get("target")
	if !validRepoName(target) {
		http.Error(w, "target name must be OWNER/NAME", http.StatusBadRequest)
		return
	}
	if err := a.store.transferRepo(repo, target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(username, "repo.transfer", target, "repository", map[string]any{"from": repo})
	http.Redirect(w, r, "/repos/"+target, http.StatusSeeOther)
}

func (a *app) branchPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/branches")
	if !validRepoName(repo) || !u.canWrite(repo) {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	var err error
	if r.PostForm.Get("action") == "delete" {
		err = a.store.deleteBranch(repo, r.PostForm.Get("branch"))
	} else {
		err = a.store.createBranch(repo, r.PostForm.Get("branch"), r.PostForm.Get("from"))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
}

func (a *app) repoAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[2] != "pulls" || !validRepoName(parts[0]+"/"+parts[1]) || !u.canRead(parts[0]+"/"+parts[1]) {
		http.NotFound(w, r)
		return
	}
	if !a.form(w, r) {
		return
	}
	repo := parts[0] + "/" + parts[1]
	if len(parts) == 3 {
		if !u.canWrite(repo) {
			http.Error(w, "write access required", http.StatusForbidden)
			return
		}
		pr, err := a.store.createPullRequestWithDraft(repo, r.PostForm.Get("title"), r.PostForm.Get("description"), username, r.PostForm.Get("source"), r.PostForm.Get("target"), r.PostForm.Get("draft") == "true")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var labels, assignees, reviewers []string
		for _, value := range strings.Split(r.PostForm.Get("labels"), ",") {
			if value = strings.TrimSpace(value); value != "" {
				labels = append(labels, value)
			}
		}
		for _, value := range strings.Split(r.PostForm.Get("assignees"), ",") {
			if value = strings.TrimSpace(value); value != "" {
				assignees = append(assignees, value)
			}
		}
		for _, value := range strings.Split(r.PostForm.Get("reviewers"), ",") {
			if value = strings.TrimSpace(value); value != "" {
				reviewers = append(reviewers, value)
			}
		}
		if len(labels) > 0 || len(assignees) > 0 {
			if _, err := a.store.updatePullMetadata(repo, pr.ID, labels, assignees); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if len(reviewers) > 0 {
			if _, err := a.store.updatePullReviewers(repo, pr.ID, reviewers); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			notifyUsers(a.store, reviewers, "pull_request.review_requested", repo, strconv.Itoa(pr.ID), username+" requested your review on pull request #"+strconv.Itoa(pr.ID))
		}
		_ = a.store.recordAudit(username, "pull_request.create", repo, "web", map[string]any{"source": r.PostForm.Get("source"), "target": r.PostForm.Get("target")})
		http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
		return
	}
	if len(parts) != 5 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	switch parts[4] {
	case "comment":
		line, _ := strconv.Atoi(r.PostForm.Get("line"))
		if _, err := a.store.addPullCommentAt(repo, id, username, r.PostForm.Get("body"), r.PostForm.Get("file"), line, r.PostForm.Get("side")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "pull_request.comment", repo, strconv.Itoa(id), nil)
	case "approve":
		if _, err := a.store.approvePullRequest(repo, id, username); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "pull_request.approve", repo, strconv.Itoa(id), nil)
	case "reviewers":
		if _, err := a.store.updatePullReviewers(repo, id, strings.Split(r.PostForm.Get("reviewers"), ",")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reviewers := []string{}
		for _, reviewer := range strings.Split(r.PostForm.Get("reviewers"), ",") {
			if reviewer = strings.TrimSpace(reviewer); reviewer != "" {
				reviewers = append(reviewers, reviewer)
			}
		}
		notifyUsers(a.store, reviewers, "pull_request.review_requested", repo, strconv.Itoa(id), username+" requested your review on pull request #"+strconv.Itoa(id))
		_ = a.store.recordAudit(username, "pull_request.reviewers", repo, strconv.Itoa(id), map[string]any{"reviewers": reviewers})
	case "ready":
		if _, err := a.store.markPullRequestReady(repo, id, username, u.Admin); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = a.store.recordAudit(username, "pull_request.ready", repo, strconv.Itoa(id), nil)
	case "merge":
		if !u.canMaintain(repo) {
			http.Error(w, "maintain access required to merge", http.StatusForbidden)
			return
		}
		if _, err := a.store.mergePullRequestWithStrategy(repo, id, username, r.PostForm.Get("strategy")); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = a.store.recordAudit(username, "pull_request.merge", repo, strconv.Itoa(id), nil)
	case "queue":
		if !u.Admin {
			http.Error(w, "admin required to queue merges", http.StatusForbidden)
			return
		}
		entry, err := a.store.enqueueMerge(repo, id, r.PostForm.Get("strategy"), username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = a.store.recordAudit(username, "merge_queue.enqueue", repo, strconv.Itoa(entry.PullRequestID), map[string]any{"strategy": entry.Strategy})
	default:
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/repos/"+repo, http.StatusSeeOther)
}

func (a *app) topicsPageAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/topics")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	if !u.canMaintain(name) {
		http.Error(w, "maintain access required", http.StatusForbidden)
		return
	}
	if !a.form(w, r) {
		return
	}
	var topics []string
	for _, item := range strings.Split(r.PostForm.Get("topics"), ",") {
		if item = strings.TrimSpace(item); item != "" {
			topics = append(topics, item)
		}
	}
	if err := a.store.setTopics(name, topics); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = a.store.recordAudit(username, "repository.topics.update", name, "web", map[string]any{"topics": topics})
	http.Redirect(w, r, "/repos/"+name, http.StatusSeeOther)
}

type issuePageData struct {
	Name     string
	CSRF     string
	CanWrite bool
	Issues   []issue
}

var issuePageTemplate = template.Must(template.New("issues").Funcs(template.FuncMap{"join": strings.Join}).Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Issues · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Issues</h1>{{if .CanWrite}}<section><h2>Open an issue</h2><form action="/repos/{{.Name}}/issues" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><label>Title <input name="title" required maxlength="200"></label></p><p><label>Labels <input name="labels" placeholder="bug, enhancement"></label></p><p><label>Description <textarea name="body" maxlength="20000" rows="5"></textarea></label></p><button type="submit">Create issue</button></form></section>{{end}}<section><h2>All issues</h2>{{range .Issues}}<article><h3>#{{.ID}} · {{.Title}}</h3><p class="muted">{{.State}} · opened by {{.Author}}{{if .Assignee}} · assigned to {{.Assignee}}{{end}}{{if .Labels}} · {{join .Labels ", "}}{{end}}</p>{{if .Body}}<pre>{{.Body}}</pre>{{end}}{{if eq .State "open"}}<form action="/repos/{{$.Name}}/issues/{{.ID}}/comment" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><textarea name="body" maxlength="20000" rows="2" placeholder="Add a comment"></textarea><button type="submit">Comment</button></form><form action="/repos/{{$.Name}}/issues/{{.ID}}/close" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit">Close issue</button></form>{{end}}{{range .Comments}}<p class="muted"><strong>{{.Author}}</strong>: {{.Body}}</p>{{end}}</article>{{else}}<p class="muted">No issues yet.</p>{{end}}</section>`))

func (a *app) issuePage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/issues")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	items, err := a.store.listIssues(name, "all")
	if err != nil {
		http.Error(w, "cannot list issues", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := issuePageTemplate.Execute(w, issuePageData{Name: name, CSRF: a.csrfFor(username), CanWrite: u.canWrite(name), Issues: items}); err != nil {
		log.Printf("render issues: %v", err)
	}
}

func (a *app) issueAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 3 || parts[2] != "issues" || !validRepoName(parts[0]+"/"+parts[1]) || !u.canRead(parts[0]+"/"+parts[1]) {
		http.NotFound(w, r)
		return
	}
	if !a.form(w, r) {
		return
	}
	repo := parts[0] + "/" + parts[1]
	if len(parts) == 3 {
		if !u.canWrite(repo) {
			http.Error(w, "write access required", http.StatusForbidden)
			return
		}
		var labels []string
		for _, label := range strings.Split(r.PostForm.Get("labels"), ",") {
			if strings.TrimSpace(label) != "" {
				labels = append(labels, strings.TrimSpace(label))
			}
		}
		if _, err := a.store.createIssue(repo, r.PostForm.Get("title"), r.PostForm.Get("body"), username, "", labels); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "issue.create", repo, "web", map[string]any{"labels": labels})
		http.Redirect(w, r, "/repos/"+repo+"/issues", http.StatusSeeOther)
		return
	}
	if len(parts) != 5 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	switch parts[4] {
	case "comment":
		if _, err := a.store.addIssueComment(repo, id, username, r.PostForm.Get("body")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "issue.comment", repo, strconv.Itoa(id), nil)
	case "close":
		if _, err := a.store.updateIssue(repo, id, username, u.Admin, "", "", "", "closed", nil); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "issue.close", repo, strconv.Itoa(id), nil)
	default:
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/repos/"+repo+"/issues", http.StatusSeeOther)
}
