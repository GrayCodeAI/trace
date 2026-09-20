package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type policyPageData struct {
	Name   string
	CSRF   string
	Admin  bool
	Policy repoPolicy
	Error  string
}

var policyPageTemplate = template.Must(template.New("policy-page").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Merge policy · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Merge policy</h1><p class="muted">Control which pull requests may be merged and which branches accept direct pushes.</p>{{if .Error}}<p class="muted">{{.Error}}</p>{{end}}<section><h2>Requirements</h2>{{if .Admin}}<form action="/repos/{{.Name}}/settings/policy" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><label>Required approvals <input name="required_approvals" type="number" min="0" max="100" value="{{.Policy.RequiredApprovals}}" required></label></p><p><label>Required successful action jobs <input name="required_checks" value="{{range $i, $check := .Policy.RequiredChecks}}{{if $i}},{{end}}{{$check}}{{end}}" placeholder="test,lint"></label></p><p><label>Protected branch patterns <input name="protected_branches" value="{{range $i, $branch := .Policy.ProtectedBranches}}{{if $i}},{{end}}{{$branch}}{{end}}" placeholder="main,release/*"></label></p><p><label><input type="checkbox" name="require_codeowners" value="true"{{if .Policy.RequireCodeOwners}} checked{{end}}> Require matching CODEOWNERS approval</label></p><button type="submit">Save policy</button></form>{{else}}<p>Only administrators can edit this policy.</p>{{end}}</section></html>`))

func (a *app) policyPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/settings/policy")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	policy, err := a.store.repoPolicy(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if !u.Admin {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}
		if !a.form(w, r) {
			return
		}
		approvals, err := strconv.Atoi(r.PostForm.Get("required_approvals"))
		if err != nil {
			http.Error(w, "required approvals must be a number", http.StatusBadRequest)
			return
		}
		var checks []string
		for _, check := range strings.Split(r.PostForm.Get("required_checks"), ",") {
			if strings.TrimSpace(check) != "" {
				checks = append(checks, strings.TrimSpace(check))
			}
		}
		var branches []string
		for _, branch := range strings.Split(r.PostForm.Get("protected_branches"), ",") {
			if strings.TrimSpace(branch) != "" {
				branches = append(branches, strings.TrimSpace(branch))
			}
		}
		policy, err = a.store.setRepoPolicy(name, repoPolicy{RequiredApprovals: approvals, RequiredChecks: checks, ProtectedBranches: branches, RequireCodeOwners: r.PostForm.Get("require_codeowners") == "true"})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = a.store.recordAudit(username, "repo.policy.update", name, "policy", map[string]any{"required_approvals": policy.RequiredApprovals, "required_checks": policy.RequiredChecks, "protected_branches": policy.ProtectedBranches, "require_codeowners": policy.RequireCodeOwners})
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := policyPageTemplate.Execute(w, policyPageData{Name: name, CSRF: a.csrfFor(username), Admin: u.Admin, Policy: policy}); err != nil {
		log.Printf("render policy page: %v", err)
	}
}
