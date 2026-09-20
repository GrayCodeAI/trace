package main

import (
	"html/template"
	"log"
	"net/http"
)

type globalSearchPageData struct {
	Query  string
	Ref    string
	Scope  string
	After  string
	Result globalSearchResponse
	Error  string
}

var globalSearchPageTemplate = template.Must(template.New("global-search").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Workspace search · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<style>
.global-wrap{max-width:1040px;margin:0 auto}.global-intro{padding:18px 0 8px}.global-intro h1{font-size:clamp(34px,5vw,52px);line-height:1.05}.global-intro p{max-width:680px;margin:.5rem 0 0}.global-form{display:grid;grid-template-columns:minmax(0,1fr) 150px 170px auto;gap:10px;align-items:end}.global-form label{display:block;margin:0;color:var(--text);font-size:12px;font-weight:700}.global-form input,.global-form select{display:block;width:100%;height:42px;min-width:0;margin:7px 0 0;padding:8px 11px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--text)}.global-form button{height:42px;margin:0;padding:0 18px;border-color:var(--accent);background:var(--accent);color:#052827}.global-form button:hover{background:var(--lime)}.global-meta{font-size:12px;margin:14px 0 0}.global-results{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px;align-items:start}.global-results.single{grid-template-columns:1fr}.global-results section{min-width:0;margin-top:18px}.global-results h2{display:flex;justify-content:space-between;align-items:center;gap:10px}.global-results h2 span{font-size:12px;font-weight:600;color:var(--muted)}.global-hit{padding:14px 0;border-top:1px solid var(--line)}.global-hit:first-of-type{margin-top:15px}.global-hit a{font-weight:700;text-decoration:none;overflow-wrap:anywhere}.global-hit a:hover{text-decoration:underline}.global-hit p{margin:5px 0 0;font-size:12px}.global-hit pre{margin:10px 0 0;max-height:150px;overflow:auto;border-color:var(--line);background:var(--bg);color:var(--text)}.global-next{display:inline-block;margin:24px 0;padding:10px 16px;border:1px solid var(--line);border-radius:8px;background:var(--panel);font-size:13px;font-weight:700;text-decoration:none}.global-error{border-color:#a94d53;color:#ffdfe0}html[data-theme="light"] .global-error{color:#842c33}html[data-theme="light"] .global-form input,html[data-theme="light"] .global-form select{background:#fff}html[data-theme="light"] .global-form button{background:var(--accent);border-color:var(--accent);color:#fff}@media(max-width:760px){.global-form{grid-template-columns:1fr 1fr}.global-form label:first-child{grid-column:1/-1}.global-form button{width:100%}.global-results{grid-template-columns:1fr}}@media(max-width:480px){.global-form{grid-template-columns:1fr}.global-form label:first-child{grid-column:auto}}
</style></head><body><div class="global-wrap"><div class="repo-nav"><a href="/app"><img src="/assets/trace-mark.svg?v=4" alt="" width="38" height="38">Trace</a><a href="/app">← Workspace</a></div><div class="global-intro"><h1>Search your workspace.</h1><p>Search literal code, commit history, and structured agent checkpoints across repositories you can access. Each page scans up to ten repositories.</p></div><section><form class="global-form" method="get" action="/search"><label>Search query<input name="q" value="{{.Query}}" required maxlength="200" autocomplete="off" placeholder="Function, decision, or commit"></label><label>Branch<input name="ref" value="{{.Ref}}" placeholder="main"></label><label>Scope<select name="scope"><option value="all"{{if eq .Scope "all"}} selected{{end}}>All sources</option><option value="code"{{if eq .Scope "code"}} selected{{end}}>Code only</option><option value="commits"{{if eq .Scope "commits"}} selected{{end}}>Commits only</option><option value="sessions"{{if eq .Scope "sessions"}} selected{{end}}>Sessions only</option></select></label><button type="submit">Search</button></form></section>{{if .Error}}<section class="global-error" role="alert">{{.Error}}</section>{{end}}{{if and .Query (not .Error)}}<p class="global-meta muted">Scanned {{.Result.ReposScanned}} repositories{{if .After}} after {{.After}}{{end}}. Results are literal; code and commits are limited to the selected branch.</p><div class="global-results{{if ne .Scope "all"}} single{{end}}">{{if or (eq .Scope "all") (eq .Scope "code")}}<section><h2>Code <span>{{len .Result.Results}} matches</span></h2>{{range .Result.Results}}<article class="global-hit"><a href="/repos/{{.Repo}}?branch={{urlquery $.Ref}}&amp;file={{urlquery .Path}}">{{.Repo}} / {{.Path}}:{{.Line}}</a><pre>{{.Content}}</pre></article>{{else}}<p class="muted">No matching code on this page.</p>{{end}}{{if .Result.CodeTruncated}}<p class="muted">A repository reached the 100 match code limit. Open its search page to narrow the query.</p>{{end}}</section>{{end}}{{if or (eq .Scope "all") (eq .Scope "commits")}}<section class="commit-section"><h2>Commits <span>{{len .Result.Commits}} matches</span></h2>{{range .Result.Commits}}<article class="global-hit"><a href="/repos/{{.Repo}}/commits/{{.Commit}}?ref={{urlquery $.Ref}}">{{.Repo}} · {{.Subject}}</a><p><code>{{printf "%.12s" .Commit}}</code> · {{.Author}}</p></article>{{else}}<p class="muted">No matching commits on this page.</p>{{end}}{{if .Result.CommitsTruncated}}<p class="muted">At least one repository reached the 1,000 commit scan or 100 match limit.</p>{{end}}</section>{{end}}{{if or (eq .Scope "all") (eq .Scope "sessions")}}<section><h2>Agent sessions <span>{{len .Result.Sessions}} matches</span></h2>{{range .Result.Sessions}}<article class="global-hit"><a href="/repos/{{.Repo}}/agents#session-{{.SessionID}}">{{.Repo}} · {{.Agent}} · session #{{.SessionID}}{{if .CheckpointID}} · checkpoint #{{.CheckpointID}}{{end}}</a><p>{{.Source}} · <code>{{.Commit}}</code></p>{{if .Excerpt}}<pre>{{.Excerpt}}</pre>{{end}}</article>{{else}}<p class="muted">No matching sessions on this page.</p>{{end}}{{if .Result.SessionsTruncated}}<p class="muted">A repository reached the 100 match session limit. Open its search page to narrow the query.</p>{{end}}</section>{{end}}</div>{{if .Result.NextAfter}}<a class="global-next" href="/search?q={{urlquery .Query}}&amp;ref={{urlquery .Ref}}&amp;scope={{urlquery .Scope}}&amp;after={{urlquery .Result.NextAfter}}">Search next repositories →</a>{{end}}{{end}}</div></body></html>`))

func (a *app) globalSearchPage(w http.ResponseWriter, r *http.Request, u userRecord) {
	query := r.URL.Query()
	data := globalSearchPageData{Query: query.Get("q"), Ref: query.Get("ref"), Scope: query.Get("scope"), After: query.Get("after")}
	if data.Ref == "" {
		data.Ref = "main"
	}
	if data.Scope == "" {
		data.Scope = "all"
	}
	if data.Query != "" {
		result, err := a.store.searchAll(u, data.Query, data.Ref, data.Scope, data.After)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Result = result
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := globalSearchPageTemplate.Execute(w, data); err != nil {
		log.Printf("render global search: %v", err)
	}
}
