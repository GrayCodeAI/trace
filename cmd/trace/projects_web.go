package main

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

type projectsPageData struct {
	Name     string
	CSRF     string
	CanWrite bool
	Projects []project
}

var projectsTemplate = template.Must(template.New("projects").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Projects · {{.Name}} · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="repo-nav"><a href="/repos/{{.Name}}">← {{.Name}}</a><a href="/app">Repositories</a></div><h1>Projects</h1>{{if .CanWrite}}<section><h2>New project</h2><form action="/repos/{{.Name}}/projects" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="create"><p><label>Name <input name="name" maxlength="120" required></label> <label>Description <input name="description" maxlength="500"></label> <button type="submit">Create project</button></p></form></section>{{end}}{{range .Projects}}{{$project := .}}<section><h2>{{$project.Name}}</h2>{{if $project.Description}}<p class="muted">{{$project.Description}}</p>{{end}}<div>{{range $project.Columns}}{{$column := .}}<article><h3>{{$column.Name}}</h3>{{range $project.Cards}}{{if eq .Column $column.Name}}<p><strong>#{{.Number}}</strong> {{.Title}} <span class="muted">({{.Kind}})</span>{{if $.CanWrite}}<form action="/repos/{{$.Name}}/projects/{{$project.ID}}/cards/{{.ID}}" method="post" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="column" value="{{.Column}}"><select name="move_to"><option value="Todo">Todo</option><option value="In progress">In progress</option><option value="Done">Done</option></select><button type="submit">Move</button></form>{{end}}</p>{{end}}{{end}}</article>{{end}}</div>{{if $.CanWrite}}<form action="/repos/{{$.Name}}/projects/{{$project.ID}}/cards" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>Kind <select name="kind"><option value="issue">Issue</option><option value="pull">Pull request</option></select></label><label>Number <input name="number" type="number" min="1" required></label><label>Title <input name="title" required></label><label>Column <select name="column"><option>Todo</option><option>In progress</option><option>Done</option></select></label><button type="submit">Add card</button></form>{{end}}</section>{{else}}<p class="muted">No projects yet.</p>{{end}}</html>`))

func (a *app) projectsPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/projects")
	if !validRepoName(name) || !u.canRead(name) {
		http.NotFound(w, r)
		return
	}
	projects, err := a.store.listProjects(name)
	if err != nil {
		http.Error(w, "cannot list projects", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := projectsTemplate.Execute(w, projectsPageData{Name: name, CSRF: a.csrfFor(username), CanWrite: u.canWrite(name), Projects: projects}); err != nil {
		http.Error(w, "cannot render projects", http.StatusInternalServerError)
	}
}

func (a *app) projectsAction(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 3 || !validRepoName(parts[0]+"/"+parts[1]) || parts[2] != "projects" || !u.canRead(parts[0]+"/"+parts[1]) {
		http.NotFound(w, r)
		return
	}
	if !a.form(w, r) {
		return
	}
	repo := parts[0] + "/" + parts[1]
	if !u.canWrite(repo) {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	if len(parts) == 3 && r.PostForm.Get("action") == "create" {
		if _, err := a.store.createProject(repo, r.PostForm.Get("name"), r.PostForm.Get("description")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if len(parts) == 5 && parts[4] == "cards" {
		id, err := strconv.Atoi(parts[3])
		number, numberErr := strconv.Atoi(r.PostForm.Get("number"))
		if err != nil || numberErr != nil {
			http.Error(w, "invalid project or card number", http.StatusBadRequest)
			return
		}
		if _, err := a.store.addProjectCard(repo, id, r.PostForm.Get("kind"), number, r.PostForm.Get("title"), r.PostForm.Get("column")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if len(parts) == 6 && parts[4] == "cards" {
		projectID, err := strconv.Atoi(parts[3])
		cardID, cardErr := strconv.Atoi(parts[5])
		if err != nil || cardErr != nil {
			http.Error(w, "invalid project or card number", http.StatusBadRequest)
			return
		}
		if _, err := a.store.moveProjectCard(repo, projectID, cardID, r.PostForm.Get("move_to")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/repos/"+repo+"/projects", http.StatusSeeOther)
}
