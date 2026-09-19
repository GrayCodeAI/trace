package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const tokenFilename = "admin-token"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "meshgit:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: meshgit <init|serve|repo|mirror> (try -help after a command)")
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("init takes no positional arguments")
		}
		return initData(*data)
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		addr := fs.String("listen", "127.0.0.1:8787", "HTTP listen address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("serve takes no positional arguments")
		}
		return serve(*data, *addr)
	case "repo":
		if len(args) < 2 || args[1] != "create" {
			return errors.New("usage: meshgit repo create [-data DIR] [-mirror] OWNER/NAME")
		}
		fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		mirror := fs.Bool("mirror", false, "mark repository as a read replica")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: meshgit repo create [-data DIR] [-mirror] OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		if err := store.createRepo(fs.Arg(0), *mirror); err != nil {
			return err
		}
		fmt.Println("created", fs.Arg(0))
		return nil
	case "mirror":
		if len(args) < 2 || args[1] != "sync" {
			return errors.New("usage: meshgit mirror sync [-data DIR] -from URL -token-file FILE OWNER/NAME")
		}
		fs := flag.NewFlagSet("mirror sync", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		from := fs.String("from", "", "source smart-HTTP Git URL")
		tokenFile := fs.String("token-file", "", "source node token file")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 1 || *from == "" || *tokenFile == "" {
			return errors.New("usage: meshgit mirror sync [-data DIR] -from URL -token-file FILE OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		return store.syncMirror(fs.Arg(0), *from, *tokenFile)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type store struct {
	root  string
	repos string
}

func openStore(data string) (*store, error) {
	root, err := filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("open data directory: %w (run meshgit init first)", err)
	}
	if !info.IsDir() {
		return nil, errors.New("data path is not a directory")
	}
	return &store{root: root, repos: filepath.Join(root, "repos")}, nil
}

func initData(data string) error {
	root, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0700); err != nil {
		return err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	path := filepath.Join(root, tokenFilename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, base64.RawURLEncoding.EncodeToString(b)); err != nil {
		return err
	}
	fmt.Printf("initialized %s\nAdmin token: %s\nStore this token securely; it grants access to all repositories.\n", root, base64.RawURLEncoding.EncodeToString(b))
	return nil
}

func validRepoName(name string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 2 && namePattern.MatchString(parts[0]) && namePattern.MatchString(parts[1])
}

func (s *store) repoPath(name string) (string, error) {
	if !validRepoName(name) {
		return "", errors.New("repository name must be OWNER/NAME using letters, digits, _ or -")
	}
	parts := strings.Split(name, "/")
	return filepath.Join(s.repos, parts[0], parts[1]+".git"), nil
}

func (s *store) createRepo(name string, mirror bool) error {
	path, err := s.repoPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("repository already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, setting := range []string{"http.getanyfile=false", "receive.denyDeletes=true"} {
		if out, err := exec.Command("git", "-C", path, "config", strings.SplitN(setting, "=", 2)[0], strings.SplitN(setting, "=", 2)[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("git config: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if mirror {
		if out, err := exec.Command("git", "-C", path, "config", "meshgit.mirror", "true").CombinedOutput(); err != nil {
			return fmt.Errorf("mark mirror: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("git", "-C", path, "config", "http.receivepack", "false").CombinedOutput(); err != nil {
			return fmt.Errorf("make mirror read-only: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (s *store) syncMirror(name, source, tokenPath string) error {
	path, err := s.repoPath(name)
	if err != nil {
		return err
	}
	u, err := url.Parse(source)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("invalid source URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return errors.New("source URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := s.createRepo(name, true); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", path, "config", "--get", "meshgit.mirror")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return errors.New("refusing to overwrite a writable repository; create it with -mirror")
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("source token file is empty")
	}
	credential := base64.StdEncoding.EncodeToString([]byte("git:" + token))
	cmd = exec.Command("git", "-C", path, "fetch", "--prune", "--force", source, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+credential, "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_TERMINAL_PROMPT=0")
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mirror fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("synced %s from %s\n", name, source)
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type app struct {
	store   *store
	token   string
	csrf    string
	gitPath string
}

func newApp(data string) (*app, error) {
	s, err := openStore(data)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(s.root, tokenFilename))
	if err != nil {
		return nil, fmt.Errorf("read admin token: %w", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, errors.New("admin token file is empty")
	}
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return nil, err
	}
	return &app{store: s, token: token, csrf: base64.RawURLEncoding.EncodeToString(csrfBytes), gitPath: gitPath}, nil
}

func serve(data, addr string) error {
	a, err := newApp(data)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: a, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Printf("meshgit listening on %s", addr)
	return srv.ListenAndServe()
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	valid := ok && subtle.ConstantTimeCompare([]byte(user), []byte("git")) == 1 && subtle.ConstantTimeCompare([]byte(pass), []byte(a.token)) == 1
	if !valid {
		w.Header().Set("WWW-Authenticate", `Basic realm="MeshGit"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		a.index(w)
	case r.URL.Path == "/repos" && r.Method == http.MethodPost:
		a.create(w, r)
	case strings.HasPrefix(r.URL.Path, "/git/"):
		a.git(w, r)
	default:
		http.NotFound(w, r)
	}
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>MeshGit</title><style>body{font:16px system-ui;max-width:720px;margin:4rem auto;padding:0 1rem;color:#17202a}h1{font-size:2rem}li{margin:.7rem 0}code{background:#f3f4f6;padding:.2rem .4rem;border-radius:4px}input,button{font:inherit;padding:.5rem}input{width:17rem}button{cursor:pointer}</style><h1>MeshGit</h1><p>Your repositories on this node.</p><form action="/repos" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>New repository <input name="name" placeholder="owner/project" required></label><button>Create</button></form><h2>Repositories</h2><ul>{{range .Names}}<li><strong>{{.}}</strong> &nbsp; <code>/git/{{.}}.git</code></li>{{else}}<li>No repositories yet.</li>{{end}}</ul><p>Clone using <code>https://YOUR_DOMAIN/git/owner/project.git</code> with username <code>git</code> and your admin token.</p></html>`))

func (a *app) index(w http.ResponseWriter) {
	owners, err := os.ReadDir(a.store.repos)
	if err != nil {
		http.Error(w, "cannot list repositories", 500)
		return
	}
	var names []string
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
			if repo.IsDir() && strings.HasSuffix(repo.Name(), ".git") && namePattern.MatchString(base) {
				names = append(names, owner.Name()+"/"+base)
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := indexTemplate.Execute(w, struct {
		Names []string
		CSRF  string
	}{names, a.csrf}); err != nil {
		log.Printf("render index: %v", err)
	}
}

func (a *app) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf")), []byte(a.csrf)) != 1 {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	if err := a.store.createRepo(r.Form.Get("name"), false); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) git(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/git/")
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	name := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if !validRepoName(name) {
		http.NotFound(w, r)
		return
	}
	// Smart HTTP needs only these routes. Refusing all others blocks path traversal
	// and direct reads of repository configuration or loose objects.
	suffix := strings.Join(parts[2:], "/")
	if suffix != "info/refs" && suffix != "git-upload-pack" && suffix != "git-receive-pack" {
		http.NotFound(w, r)
		return
	}
	path, _ := a.store.repoPath(name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	copyReq := r.Clone(r.Context())
	copyReq.Header.Del("Authorization")
	h := &cgi.Handler{Path: a.gitPath, Args: []string{"http-backend"}, Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + a.store.repos, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=git"}, Stderr: io.Discard}
	h.ServeHTTP(w, copyReq)
}
