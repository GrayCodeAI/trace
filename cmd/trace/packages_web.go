package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
)

type packagesPageData struct {
	Name     string
	Packages []packageArtifact
}

var packagesPageTemplate = template.Must(template.New("packages").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Packages · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Packages</h1><p class="muted">Immutable package artifacts published to this repository.</p><section>{{range .Packages}}<article><h2>{{.Name}}@{{.Version}}</h2><p><code>{{.Filename}}</code> · {{.Size}} bytes · SHA-256 <code>{{.SHA256}}</code></p><p class="muted">Published by {{.Publisher}} · {{.CreatedAt}}</p><a href="/packages/{{.Repo}}/{{.Name}}/{{.Version}}/{{.Filename}}">Download</a></article>{{else}}<p class="muted">No packages published yet.</p>{{end}}</section></html>`))

func (a *app) packagesPage(w http.ResponseWriter, r *http.Request, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/packages")
	if r.Method != http.MethodGet || !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	items, err := a.store.listPackages(name)
	if err != nil {
		http.Error(w, "cannot list packages", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := packagesPageTemplate.Execute(w, packagesPageData{Name: name, Packages: items}); err != nil {
		log.Printf("render packages: %v", err)
	}
}
