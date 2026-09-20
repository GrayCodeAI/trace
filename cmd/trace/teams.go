package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

type teamRecord struct {
	Members map[string]bool   `json:"members"`
	Repos   map[string]string `json:"repos,omitempty"`
}

type teamDB struct {
	Teams map[string]teamRecord `json:"teams"`
}

type teamView struct {
	Name    string
	Members []string
	Repos   []string
}

var teamsPageTemplate = template.Must(template.New("teams-page").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Teams · Trace</title><link rel="icon" href="/assets/trace-mark.svg?v=4" type="image/svg+xml">` + pageStyle + `<div class="topline"><h1><img src="/assets/trace-mark.svg?v=4" alt=""> Teams</h1><p><a href="/app">Back to workspace</a></p></div><p>Group repository access for small teams. Members inherit the highest role granted by any team.</p><section><h2>Create team</h2><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="create"><label>Name <input name="team" required pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,62}"></label><button>Create team</button></form></section>{{range .Teams}}<section><h2>{{.Name}}</h2>{{$team := .}}<p><strong>Members:</strong> {{if .Members}}{{range .Members}}<code>{{.}}</code> <form method="post" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="remove-member"><input type="hidden" name="team" value="{{$team.Name}}"><input type="hidden" name="user" value="{{.}}"><button>Remove {{.}}</button></form> {{end}}{{else}}none{{end}}</p><p><strong>Repositories:</strong> {{if .Repos}}{{range .Repos}}<code>{{.}}</code> {{end}}{{else}}none{{end}}</p><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="add-member"><input type="hidden" name="team" value="{{$team.Name}}"><label>Add member <input name="user" required></label><button>Add</button></form><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="grant"><input type="hidden" name="team" value="{{$team.Name}}"><label>Repository <input name="repo" placeholder="owner/name" required></label><label>Role <select name="role"><option>read</option><option>write</option><option>maintain</option><option>none</option></select></label><button>Save repository access</button></form><form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="delete"><input type="hidden" name="team" value="{{$team.Name}}"><button>Delete team</button></form></section>{{else}}<section><p>No teams configured.</p></section>{{end}}</html>`))

func (a *app) teamsPage(w http.ResponseWriter, r *http.Request, username string, u userRecord) {
	if !u.Admin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		if !a.form(w, r) {
			return
		}
		var err error
		action, team := r.PostForm.Get("action"), r.PostForm.Get("team")
		switch action {
		case "create":
			err = a.store.createTeam(team)
		case "delete":
			err = a.store.deleteTeam(team)
		case "add-member":
			err = a.store.updateTeamMember(team, r.PostForm.Get("user"), true)
		case "remove-member":
			err = a.store.updateTeamMember(team, r.PostForm.Get("user"), false)
		case "grant":
			err = a.store.grantTeam(team, r.PostForm.Get("repo"), r.PostForm.Get("role"))
		default:
			err = errors.New("unknown team action")
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/settings/teams", http.StatusSeeOther)
		return
	}
	db, err := a.store.loadTeams()
	if err != nil {
		http.Error(w, "cannot load teams", http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(db.Teams))
	for name := range db.Teams {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]teamView, 0, len(names))
	for _, name := range names {
		record := db.Teams[name]
		members := make([]string, 0, len(record.Members))
		for member := range record.Members {
			members = append(members, member)
		}
		sort.Strings(members)
		repos := make([]string, 0, len(record.Repos))
		for repo, role := range record.Repos {
			repos = append(repos, repo+" ("+role+")")
		}
		sort.Strings(repos)
		views = append(views, teamView{Name: name, Members: members, Repos: repos})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = teamsPageTemplate.Execute(w, struct {
		Username string
		CSRF     string
		Teams    []teamView
	}{Username: username, CSRF: a.csrfFor(username), Teams: views})
}

func (s *store) loadTeams() (teamDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "teams.json"))
	if errors.Is(err, os.ErrNotExist) {
		return teamDB{Teams: map[string]teamRecord{}}, nil
	}
	if err != nil {
		return teamDB{}, err
	}
	var db teamDB
	if err := json.Unmarshal(b, &db); err != nil {
		return teamDB{}, fmt.Errorf("read teams: %w", err)
	}
	if db.Teams == nil {
		db.Teams = map[string]teamRecord{}
	}
	return db, nil
}

func (s *store) updateTeams(change func(*teamDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".teams.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadTeams()
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
	tmp, err := os.CreateTemp(s.root, ".teams-")
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
	return os.Rename(name, filepath.Join(s.root, "teams.json"))
}

func validateTeamName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("invalid team name")
	}
	return nil
}

func (s *store) createTeam(name string) error {
	if err := validateTeamName(name); err != nil {
		return err
	}
	return s.updateTeams(func(db *teamDB) error {
		if _, exists := db.Teams[name]; exists {
			return errors.New("team already exists")
		}
		db.Teams[name] = teamRecord{Members: map[string]bool{}, Repos: map[string]string{}}
		return nil
	})
}

func (s *store) deleteTeam(name string) error {
	return s.updateTeams(func(db *teamDB) error {
		if _, exists := db.Teams[name]; !exists {
			return errors.New("team not found")
		}
		delete(db.Teams, name)
		return nil
	})
}

func (s *store) updateTeamMember(team, user string, present bool) error {
	if err := validateTeamName(team); err != nil {
		return err
	}
	if !namePattern.MatchString(user) || user == "git" {
		return errors.New("invalid username")
	}
	if _, err := s.loadUsers(); err != nil {
		return err
	}
	return s.updateTeams(func(db *teamDB) error {
		record, ok := db.Teams[team]
		if !ok {
			return errors.New("team not found")
		}
		if record.Members == nil {
			record.Members = map[string]bool{}
		}
		if present {
			record.Members[user] = true
		} else {
			delete(record.Members, user)
		}
		db.Teams[team] = record
		return nil
	})
}

func (s *store) grantTeam(team, repo, role string) error {
	if err := validateTeamName(team); err != nil {
		return err
	}
	if !validRepoName(repo) || (role != "read" && role != "write" && role != "maintain" && role != "none") {
		return errors.New("repository and role are invalid")
	}
	return s.updateTeams(func(db *teamDB) error {
		record, ok := db.Teams[team]
		if !ok {
			return errors.New("team not found")
		}
		if record.Repos == nil {
			record.Repos = map[string]string{}
		}
		if role == "none" {
			delete(record.Repos, repo)
		} else {
			record.Repos[repo] = role
		}
		db.Teams[team] = record
		return nil
	})
}

func roleRank(role string) int {
	switch role {
	case "write":
		return 2
	case "maintain":
		return 3
	case "read":
		return 1
	default:
		return 0
	}
}

func (s *store) expandUser(username string, u userRecord) userRecord {
	db, err := s.loadTeams()
	if err != nil {
		return u
	}
	if u.Repos == nil {
		u.Repos = map[string]string{}
	}
	for _, team := range db.Teams {
		if !team.Members[username] {
			continue
		}
		for repo, role := range team.Repos {
			if roleRank(role) > roleRank(u.Repos[repo]) {
				u.Repos[repo] = role
			}
		}
	}
	return u
}

func teamCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace team <create|delete|add|remove|grant|list> ...")
	}
	fs := flag.NewFlagSet("team "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create", "delete":
		if fs.NArg() != 1 {
			return errors.New("usage: trace team <create|delete> [-data DIR] TEAM")
		}
		if args[0] == "create" {
			err = s.createTeam(fs.Arg(0))
		} else {
			err = s.deleteTeam(fs.Arg(0))
		}
	case "add", "remove":
		if fs.NArg() != 2 {
			return errors.New("usage: trace team <add|remove> [-data DIR] TEAM USER")
		}
		err = s.updateTeamMember(fs.Arg(0), fs.Arg(1), args[0] == "add")
	case "grant":
		if fs.NArg() != 3 {
			return errors.New("usage: trace team grant [-data DIR] TEAM OWNER/NAME ROLE")
		}
		err = s.grantTeam(fs.Arg(0), fs.Arg(1), fs.Arg(2))
	case "list":
		if fs.NArg() != 0 {
			return errors.New("usage: trace team list [-data DIR]")
		}
		db, loadErr := s.loadTeams()
		if loadErr != nil {
			return loadErr
		}
		teams := make([]string, 0, len(db.Teams))
		for name := range db.Teams {
			teams = append(teams, name)
		}
		sort.Strings(teams)
		for _, name := range teams {
			record := db.Teams[name]
			fmt.Printf("%s members=%d repos=%d\n", name, len(record.Members), len(record.Repos))
		}
		return nil
	default:
		return errors.New("usage: trace team <create|delete|add|remove|grant|list> ...")
	}
	if err != nil {
		return err
	}
	fmt.Println(args[0], "complete")
	return nil
}

func (a *app) apiTeams(w http.ResponseWriter, r *http.Request, u userRecord, tail []string) {
	if !u.Admin {
		apiError(w, http.StatusForbidden, "admin required")
		return
	}
	if len(tail) == 0 {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		db, err := a.store.loadTeams()
		if err != nil {
			apiError(w, http.StatusInternalServerError, "cannot load teams")
			return
		}
		writeJSON(w, http.StatusOK, db.Teams)
		return
	}
	team := tail[0]
	if len(tail) == 1 && r.Method == http.MethodPost {
		if err := a.store.createTeam(team); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"team": team})
		return
	}
	if len(tail) == 1 && r.Method == http.MethodDelete {
		if err := a.store.deleteTeam(team); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(tail) == 2 && tail[1] == "members" && r.Method == http.MethodPost {
		var input struct {
			User string `json:"user"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.store.updateTeamMember(team, input.User, true); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"team": team, "user": input.User})
		return
	}
	if len(tail) == 2 && tail[1] == "repos" && r.Method == http.MethodPost {
		var input struct {
			Repo string `json:"repo"`
			Role string `json:"role"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.store.grantTeam(team, input.Repo, input.Role); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, input)
		return
	}
	apiError(w, http.StatusNotFound, "team route not found")
}
