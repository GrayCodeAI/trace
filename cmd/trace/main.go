package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const tokenFilename = "admin-token"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "trace:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace <init|serve|repo|mirror|agent|user|package|backup|api> (try -help after a command)")
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
		sshAddr := fs.String("ssh-listen", "", "SSH Git listen address (disabled by default)")
		federationInterval := fs.Duration("federation-interval", 0, "background federation peer sync interval (disabled by default)")
		actionsInterval := fs.Duration("actions-interval", 0, "scheduled workflow evaluation interval (disabled by default)")
		webhookPrivate := fs.Bool("webhook-allow-private-networks", false, "allow webhook deliveries to private-network addresses (link-local and metadata addresses stay blocked)")
		trustedProxy := fs.String("trusted-proxy", "", "comma-separated IPs or CIDR prefixes of reverse proxies whose X-Forwarded-For/X-Real-IP identify clients for rate limiting")
		actionsMode := fs.String("actions", actionsModeSandboxed, "CI runner policy: off, sandboxed (only workflows with \"sandbox\":true), or trusted (also run unsandboxed workflows as the Trace service account)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("serve takes no positional arguments")
		}
		if *federationInterval < 0 {
			return errors.New("-federation-interval cannot be negative")
		}
		if *actionsInterval < 0 {
			return errors.New("-actions-interval cannot be negative")
		}
		mode, err := parseActionsMode(*actionsMode)
		if err != nil {
			return err
		}
		proxies, err := parseTrustedProxies(*trustedProxy)
		if err != nil {
			return err
		}
		return serve(serveOptions{data: *data, addr: *addr, sshAddr: *sshAddr, federationInterval: *federationInterval, actionsInterval: *actionsInterval, actionsMode: mode, trustedProxies: proxies, webhookAllowPrivate: *webhookPrivate})
	case "repo":
		if len(args) < 2 || (args[1] != "create" && args[1] != "import" && args[1] != "fork" && args[1] != "archive" && args[1] != "restore" && args[1] != "delete" && args[1] != "transfer" && args[1] != "topics") {
			return errors.New("usage: trace repo <create|import|fork|archive|restore|delete|transfer|topics> ...")
		}
		if args[1] == "topics" {
			if len(args) < 3 || (args[2] != "list" && args[2] != "set") {
				return errors.New("usage: trace repo topics <list|set> [-data DIR] OWNER/NAME [topic,...]")
			}
			fs := flag.NewFlagSet("repo topics", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[3:]); err != nil {
				return err
			}
			if fs.NArg() < 1 || fs.NArg() > 2 || !validRepoName(fs.Arg(0)) || (args[2] == "set" && fs.NArg() != 2) {
				return errors.New("usage: trace repo topics <list|set> [-data DIR] OWNER/NAME [topic,...]")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if args[2] == "list" {
				topics, err := store.getTopics(fs.Arg(0))
				if err != nil {
					return err
				}
				fmt.Println(strings.Join(topics, ","))
				return nil
			}
			values := []string{}
			if fs.Arg(1) != "" {
				values = strings.Split(fs.Arg(1), ",")
			}
			if err := store.setTopics(fs.Arg(0), values); err != nil {
				return err
			}
			fmt.Println("updated topics for", fs.Arg(0))
			return nil
		}
		if args[1] == "import" {
			fs := flag.NewFlagSet("repo import", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 2 {
				return errors.New("usage: trace repo import [-data DIR] SOURCE_PATH OWNER/NAME")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if err := store.importRepo(fs.Arg(0), fs.Arg(1)); err != nil {
				return err
			}
			fmt.Println("imported", fs.Arg(1))
			return nil
		}
		if args[1] == "archive" || args[1] == "restore" || args[1] == "delete" {
			fs := flag.NewFlagSet("repo "+args[1], flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
				return errors.New("usage: trace repo <archive|restore|delete> [-data DIR] OWNER/NAME")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if args[1] == "delete" {
				if err := store.deleteRepo(fs.Arg(0)); err != nil {
					return err
				}
			} else if err := store.setArchived(fs.Arg(0), args[1] == "archive"); err != nil {
				return err
			}
			fmt.Println(args[1]+"d", fs.Arg(0))
			return nil
		}
		if args[1] == "transfer" {
			fs := flag.NewFlagSet("repo transfer", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || !validRepoName(fs.Arg(1)) {
				return errors.New("usage: trace repo transfer [-data DIR] OWNER/NAME NEW_OWNER/NAME")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if err := store.transferRepo(fs.Arg(0), fs.Arg(1)); err != nil {
				return err
			}
			fmt.Println("transferred", fs.Arg(0), "to", fs.Arg(1))
			return nil
		}
		if args[1] == "fork" {
			fs := flag.NewFlagSet("repo fork", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 2 {
				return errors.New("usage: trace repo fork [-data DIR] SOURCE/OWNER TARGET/OWNER")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if err := store.forkRepo(fs.Arg(0), fs.Arg(1)); err != nil {
				return err
			}
			fmt.Println("forked", fs.Arg(0), "as", fs.Arg(1))
			return nil
		}
		fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		mirror := fs.Bool("mirror", false, "mark repository as a read replica")
		public := fs.Bool("public", false, "make repository publicly readable")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: trace repo create [-data DIR] [-mirror] OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		if err := store.createRepo(fs.Arg(0), *mirror); err != nil {
			return err
		}
		if *public {
			if err := store.setPublic(fs.Arg(0), true); err != nil {
				return err
			}
		}
		fmt.Println("created", fs.Arg(0))
		return nil
	case "mirror":
		if len(args) < 2 || args[1] != "sync" {
			return errors.New("usage: trace mirror sync [-data DIR] -from URL -user USER -token-file FILE OWNER/NAME")
		}
		fs := flag.NewFlagSet("mirror sync", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		from := fs.String("from", "", "source smart-HTTP Git URL")
		sourceUser := fs.String("user", "admin", "username on source node")
		tokenFile := fs.String("token-file", "", "source node token file")
		verifyManifest := fs.Bool("verify-manifest", true, "verify the source node's signed ref manifest before accepting refs")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 1 || *from == "" || *tokenFile == "" {
			return errors.New("usage: trace mirror sync [-data DIR] -from URL -user USER -token-file FILE OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		return store.syncMirrorWithManifest(fs.Arg(0), *from, *sourceUser, *tokenFile, *verifyManifest)
	case "user":
		return userCommand(args[1:])
	case "agent":
		return agentCommand(args[1:])
	case "ssh":
		if len(args) < 2 || (args[1] != "host-key" && args[1] != "rotate-host-key") {
			return errors.New("usage: trace ssh <host-key|rotate-host-key> [-data DIR]")
		}
		fs := flag.NewFlagSet("ssh "+args[1], flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("usage: trace ssh " + args[1] + " [-data DIR]")
		}
		publicKey, fingerprint, err := sshHostKeyInfo(*data)
		if args[1] == "rotate-host-key" {
			publicKey, fingerprint, err = rotateSSHHostKey(*data)
		}
		if err != nil {
			return err
		}
		fmt.Printf("Host key: %sFingerprint: %s\n", publicKey, fingerprint)
		return nil
	case "sso":
		if len(args) < 3 || args[1] != "oidc" {
			return errors.New("usage: trace sso oidc <set|disable|link|unlink> ...")
		}
		return oidcCommand(args[2:])
	case "team":
		return teamCommand(args[1:])
	case "project":
		return projectCommand(args[1:])
	case "package":
		return packageCommand(args[1:])
	case "backup":
		return backupCommand(args[1:])
	case "pages":
		if len(args) < 2 || (args[1] != "enable" && args[1] != "disable") {
			return errors.New("usage: trace pages <enable|disable> [-data DIR] OWNER/NAME [BRANCH [ROOT]]")
		}
		fs := flag.NewFlagSet("pages "+args[1], flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if args[1] == "enable" && (fs.NArg() < 2 || fs.NArg() > 3) {
			return errors.New("usage: trace pages enable [-data DIR] OWNER/NAME BRANCH [ROOT]")
		}
		if args[1] == "disable" && fs.NArg() != 1 {
			return errors.New("usage: trace pages disable [-data DIR] OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		branch, root := "", ""
		if args[1] == "enable" {
			branch = fs.Arg(1)
			if fs.NArg() == 3 {
				root = fs.Arg(2)
			}
		}
		if err := store.setPages(fs.Arg(0), branch, root, args[1] == "enable"); err != nil {
			return err
		}
		fmt.Println("pages", args[1], fs.Arg(0))
		return nil
	case "api":
		return apiCommand(args[1:])
	case "branch":
		if len(args) < 2 || (args[1] != "create" && args[1] != "delete") {
			return errors.New("usage: trace branch <create|delete> ...")
		}
		fs := flag.NewFlagSet("branch "+args[1], flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		from := fs.String("from", "main", "source branch for create")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 2 && (args[1] == "create" || fs.NArg() != 2) {
			return errors.New("usage: trace branch <create|delete> [-data DIR] OWNER/NAME BRANCH [-from SOURCE]")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		if args[1] == "create" {
			if err := store.createBranch(fs.Arg(0), fs.Arg(1), *from); err != nil {
				return err
			}
		} else if err := store.deleteBranch(fs.Arg(0), fs.Arg(1)); err != nil {
			return err
		}
		fmt.Println(args[1]+"d", fs.Arg(0), fs.Arg(1))
		return nil
	case "visibility":
		if len(args) < 2 || (args[1] != "public" && args[1] != "private") {
			return errors.New("usage: trace visibility <public|private> [-data DIR] OWNER/NAME")
		}
		fs := flag.NewFlagSet("visibility", flag.ContinueOnError)
		data := fs.String("data", "./data", "data directory")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: trace visibility <public|private> [-data DIR] OWNER/NAME")
		}
		store, err := openStore(*data)
		if err != nil {
			return err
		}
		return store.setPublic(fs.Arg(0), args[1] == "public")
	case "federation":
		if len(args) >= 3 && args[1] == "trust" {
			fs := flag.NewFlagSet("federation trust", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			expected := fs.String("expected", "", "currently pinned node ID required for forget")
			if err := fs.Parse(args[3:]); err != nil {
				return err
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			switch args[2] {
			case "list":
				if fs.NArg() != 0 {
					return errors.New("usage: trace federation trust list [-data DIR]")
				}
				pins, err := store.loadFederationTrust()
				if err != nil {
					return err
				}
				b, _ := json.MarshalIndent(pins, "", "  ")
				fmt.Println(string(b))
				return nil
			case "forget":
				if fs.NArg() != 2 || *expected == "" {
					return errors.New("usage: trace federation trust forget [-data DIR] -expected NODE_ID OWNER/NAME SOURCE_URL")
				}
				return store.forgetFederationIdentity(fs.Arg(0), fs.Arg(1), *expected)
			default:
				return errors.New("usage: trace federation trust <list|forget> ...")
			}
		}
		if len(args) >= 2 && args[1] == "conflicts" {
			fs := flag.NewFlagSet("federation conflicts", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 0 {
				return errors.New("usage: trace federation conflicts [-data DIR]")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			conflicts, err := store.loadFederationConflicts()
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(conflicts, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		if len(args) >= 2 && args[1] == "resolve" {
			fs := flag.NewFlagSet("federation resolve", flag.ContinueOnError)
			data := fs.String("data", "./data", "data directory")
			source := fs.String("source", "", "source Git URL from the conflict record")
			ref := fs.String("ref", "", "full Git ref from the conflict record")
			localSHA := fs.String("local", "", "current mirror object ID from the conflict record")
			remoteSHA := fs.String("remote", "", "signed source object ID from the conflict record")
			deleteRef := fs.Bool("delete", false, "accept deletion of a ref absent from the signed source")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 1 || *source == "" || *ref == "" || *localSHA == "" || (*deleteRef && *remoteSHA != "") || (!*deleteRef && *remoteSHA == "") {
				return errors.New("usage: trace federation resolve [-data DIR] -source URL -ref FULL_REF -local OID (-remote OID|-delete) OWNER/NAME")
			}
			store, err := openStore(*data)
			if err != nil {
				return err
			}
			if err := store.approveFederationConflict(fs.Arg(0), *source, *ref, *localSHA, *remoteSHA, "local-admin"); err != nil {
				return err
			}
			fmt.Println("approved signed source for", fs.Arg(0), *ref, "at the recorded object IDs; retry mirror sync")
			return nil
		}
		if len(args) < 2 || args[1] != "peer" {
			return errors.New("usage: trace federation <peer <add|list|remove|sync>|conflicts|resolve|trust <list|forget>> ...")
		}
		return federationPeerCommand(args[2:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type store struct {
	root  string
	repos string
	// actionsMode is the operator's CI runner policy (see parseActionsMode).
	// The zero value means actionsModeSandboxed.
	actionsMode string
	// webhookAllowPrivate lets webhooks target private-network addresses.
	webhookAllowPrivate bool
}

func openStore(data string) (*store, error) {
	root, err := filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("open data directory: %w (run trace init first)", err)
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
		if out, err := exec.Command("git", "-C", path, "config", "trace.mirror", "true").CombinedOutput(); err != nil {
			return fmt.Errorf("mark mirror: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("git", "-C", path, "config", "http.receivepack", "false").CombinedOutput(); err != nil {
			return fmt.Errorf("make mirror read-only: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return installHook(path)
}

func (s *store) importRepo(source, name string) error {
	if !validRepoName(name) {
		return errors.New("repository name must be OWNER/NAME using letters, digits, _ or -")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(source); statErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("source path does not exist")
	}
	target, err := s.repoPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return errors.New("repository already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", "--bare", "--no-hardlinks", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("import repository: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, setting := range []string{"http.getanyfile=false", "receive.denyDeletes=true"} {
		parts := strings.SplitN(setting, "=", 2)
		if out, err := exec.Command("git", "-C", target, "config", parts[0], parts[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("configure imported repository: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if err := installHook(target); err != nil {
		return err
	}
	return nil
}

// deleteRepo permanently removes a bare repository from this node. Callers
// must provide the admin authorization at the API/web layer before reaching
// this store method; the CLI is intended for the local administrator.
func (s *store) deleteRepo(name string) error {
	path, err := s.repoPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return errors.New("repository not found")
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete repository: %w", err)
	}
	return nil
}

func (s *store) transferRepo(source, target string) error {
	if !validRepoName(source) || !validRepoName(target) {
		return errors.New("repository names must be OWNER/NAME")
	}
	if source == target {
		return errors.New("source and target repository are the same")
	}
	sourcePath, err := s.repoPath(source)
	if err != nil {
		return err
	}
	targetPath, err := s.repoPath(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return errors.New("source repository not found")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return errors.New("target repository already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return err
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("transfer repository: %w", err)
	}
	if err := s.rewriteRepoReferences(source, target); err != nil {
		return fmt.Errorf("rewrite repository references: %w", err)
	}
	return nil
}

func (s *store) rewriteRepoReferences(source, target string) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !(strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".jsonl")) {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		updated := bytes.ReplaceAll(b, []byte(`"`+source+`"`), []byte(`"`+target+`"`))
		if bytes.Equal(updated, b) {
			continue
		}
		if err := os.WriteFile(path, updated, 0600); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) forkRepo(source, target string) error {
	if !validRepoName(source) || !validRepoName(target) {
		return errors.New("repository names must be OWNER/NAME")
	}
	sourcePath, err := s.repoPath(source)
	if err != nil {
		return err
	}
	targetPath, err := s.repoPath(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return errors.New("source repository not found")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return errors.New("target repository already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", "--bare", "--no-hardlinks", sourcePath, targetPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone fork: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, setting := range []string{"http.getanyfile=false", "receive.denyDeletes=true"} {
		if out, err := exec.Command("git", "-C", targetPath, "config", strings.SplitN(setting, "=", 2)[0], strings.SplitN(setting, "=", 2)[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("configure fork: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return installHook(targetPath)
}

// createRepoFromTemplate copies Git history into a new independent repository.
// Unlike a fork, it does not create or retain any relationship metadata.
func (s *store) createRepoFromTemplate(source, target string) error {
	return s.forkRepo(source, target)
}

func (s *store) syncMirror(name, source, sourceUser, tokenPath string) error {
	return s.syncMirrorWithManifest(name, source, sourceUser, tokenPath, false)
}

func (s *store) syncMirrorWithManifest(name, source, sourceUser, tokenPath string, verifyManifest bool) error {
	if !namePattern.MatchString(sourceUser) {
		return errors.New("invalid source username")
	}
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
	if !isMirror(path) {
		return errors.New("refusing to overwrite a writable repository; create it with -mirror")
	}
	unlock, err := lockMirrorSync(path)
	if err != nil {
		return fmt.Errorf("lock mirror sync: %w", err)
	}
	defer unlock()
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("source token file is empty")
	}
	credential := base64.StdEncoding.EncodeToString([]byte(sourceUser + ":" + token))
	var expected *signedRepositoryManifest
	if verifyManifest {
		manifest, err := fetchRemoteManifest(source, name, sourceUser, token)
		if err != nil {
			return fmt.Errorf("verify source manifest: %w", err)
		}
		if err := verifyRepositoryManifest(manifest); err != nil {
			return fmt.Errorf("verify source manifest: %w", err)
		}
		if manifest.Payload.Repo != name {
			return errors.New("source manifest repository does not match mirror repository")
		}
		if err := s.checkOrPinFederationIdentity(name, source, manifest.Payload.NodeID); err != nil {
			return err
		}
		expected = &manifest
	}
	if expected != nil {
		// Download source objects into a private trace namespace first. This
		// lets us compare the fetched refs against the signed manifest before
		// any mirror refs can be replaced.
		cleanupFederationProbe(path)
		probe := exec.Command("git", "-C", path, "fetch", "--no-tags", "--force", source, "+refs/heads/*:refs/trace/remote/heads/*", "+refs/tags/*:refs/trace/remote/tags/*")
		probe.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+credential, "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_TERMINAL_PROMPT=0")
		if probeOut, probeErr := probe.CombinedOutput(); probeErr != nil {
			return fmt.Errorf("probe source refs: %w: %s", probeErr, strings.TrimSpace(string(probeOut)))
		}
		defer cleanupFederationProbe(path)
		if err := verifyFetchedManifestRefs(path, expected.Payload.Refs); err != nil {
			return err
		}
		local, err := s.checkFederationConflicts(name, source, expected.Payload.Refs)
		if err != nil {
			return err
		}
		if err := applySignedProbeRefs(path, expected.Payload.Refs, local); err != nil {
			return err
		}
		actual, err := s.repositoryRefs(name)
		if err != nil {
			return fmt.Errorf("read mirrored refs: %w", err)
		}
		if !reflect.DeepEqual(actual, expected.Payload.Refs) {
			return errors.New("mirrored refs differ from the signed source manifest; inspect mirror state")
		}
		if err := s.completeFederationResolutions(name, source, expected.Payload.Refs, local); err != nil {
			return fmt.Errorf("record applied federation resolutions: %w", err)
		}
		fmt.Printf("synced %s from %s\n", name, source)
		return nil
	}
	cmd := exec.Command("git", "-C", path, "fetch", "--prune", "--force", source, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+credential, "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mirror fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("synced %s from %s\n", name, source)
	return nil
}

func isMirror(path string) bool {
	for _, key := range []string{"trace.mirror", "refweave.mirror"} {
		out, err := exec.Command("git", "-C", path, "config", "--get", key).Output()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return true
		}
	}
	return false
}

func isArchived(path string) bool {
	out, err := exec.Command("git", "-C", path, "config", "--get", "trace.archived").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func isPublic(path string) bool {
	out, err := exec.Command("git", "-C", path, "config", "--get", "trace.public").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func (s *store) setPublic(name string, public bool) error {
	path, err := s.repoPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("repository not found")
	}
	if public {
		refs, truncated, err := gitOutput(path, 4096, "for-each-ref", "--format=%(refname)", agentGitRefPrefix)
		if err != nil || truncated {
			return errors.New("cannot inspect Git agent history before making repository public")
		}
		if strings.TrimSpace(refs) != "" {
			return errors.New("remove published Git agent history before making repository public; old Git objects and fetched copies may still exist")
		}
	}
	value := "false"
	if public {
		value = "true"
	}
	if out, err := exec.Command("git", "-C", path, "config", "trace.public", value).CombinedOutput(); err != nil {
		return fmt.Errorf("set visibility: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *store) setArchived(name string, archived bool) error {
	path, err := s.repoPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("repository not found")
	}
	value := "false"
	if archived {
		value = "true"
	}
	if out, err := exec.Command("git", "-C", path, "config", "trace.archived", value).CombinedOutput(); err != nil {
		return fmt.Errorf("set archive status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func validManagedBranchName(name string) bool {
	return namePattern.MatchString(name) && name != "main"
}

func (s *store) createBranch(repo, branch, from string) error {
	if !validManagedBranchName(branch) || strings.TrimSpace(from) == "" {
		return errors.New("branch must use letters, digits, _ or - and cannot be main")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if isArchived(path) || isMirror(path) {
		return errors.New("repository is read-only")
	}
	if _, err := exec.Command("git", "-C", path, "rev-parse", "--verify", "refs/heads/"+from).Output(); err != nil {
		return errors.New("source branch not found")
	}
	if _, err := exec.Command("git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).CombinedOutput(); err == nil {
		return errors.New("branch already exists")
	}
	if out, err := exec.Command("git", "-C", path, "branch", branch, from).CombinedOutput(); err != nil {
		return fmt.Errorf("create branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *store) deleteBranch(repo, branch string) error {
	if !validManagedBranchName(branch) {
		return errors.New("cannot delete main or invalid branch")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if isArchived(path) || isMirror(path) {
		return errors.New("repository is read-only")
	}
	if out, err := exec.Command("git", "-C", path, "branch", "-D", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("delete branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
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
	store      *store
	csrf       string
	sessionKey []byte
	gitPath    string
	limiter    *rateLimiter
}

func newApp(data string) (*app, error) {
	s, err := openStore(data)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadUsers(); err != nil {
		return nil, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return nil, err
	}
	sessionKey := make([]byte, 32)
	if _, err := rand.Read(sessionKey); err != nil {
		return nil, err
	}
	return &app{store: s, csrf: base64.RawURLEncoding.EncodeToString(csrfBytes), sessionKey: sessionKey, gitPath: gitPath, limiter: newRateLimiter(s.root)}, nil
}

// serveOptions carries the operator's `trace serve` flags.
type serveOptions struct {
	data               string
	addr               string
	sshAddr            string
	federationInterval time.Duration
	actionsInterval    time.Duration
	actionsMode        string
	trustedProxies     []netip.Prefix
	// webhookAllowPrivate permits webhook targets in private networks.
	webhookAllowPrivate bool
}

func serve(opts serveOptions) error {
	data, addr, sshAddr := opts.data, opts.addr, opts.sshAddr
	federationInterval, actionsInterval := opts.federationInterval, opts.actionsInterval
	a, err := newApp(data)
	if err != nil {
		return err
	}
	a.store.actionsMode = opts.actionsMode
	a.limiter.trustedProxies = opts.trustedProxies
	a.store.webhookAllowPrivate = opts.webhookAllowPrivate
	log.Printf("trace CI actions mode: %s", a.store.actionsPolicy())
	if err := a.store.ensureHooks(); err != nil {
		return err
	}
	if sshAddr != "" {
		go func() {
			if err := serveSSH(a.store, sshAddr); err != nil {
				log.Printf("trace SSH stopped: %v", err)
			}
		}()
	}
	if federationInterval > 0 {
		go func() {
			ticker := time.NewTicker(federationInterval)
			defer ticker.Stop()
			for range ticker.C {
				peers, err := a.store.loadFederationPeers()
				if err != nil {
					log.Printf("trace federation peer load failed: %v", err)
					continue
				}
				if len(peers) == 0 {
					continue
				}
				if err := a.store.syncFederationPeers(""); err != nil {
					log.Printf("trace federation sync failed: %v", err)
				}
			}
		}()
		log.Printf("trace federation background sync every %s", federationInterval)
	}
	if actionsInterval > 0 {
		go func() {
			ticker := time.NewTicker(actionsInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := a.store.scheduleActionRuns(); err != nil {
					log.Printf("trace scheduled actions failed: %v", err)
				}
			}
		}()
		log.Printf("trace scheduled workflow evaluation every %s", actionsInterval)
	}
	srv := &http.Server{Addr: addr, Handler: a, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Printf("trace listening on %s", addr)
	return srv.ListenAndServe()
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decision := a.limiter.allow(r)
	writeRateHeaders(w, decision)
	if !decision.Allowed {
		w.Header().Set("Retry-After", fmt.Sprint(int(time.Until(decision.Reset).Seconds())+1))
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", appContentSecurityPolicy)
	switch {
	case r.URL.Path == "/.well-known/trace/ssh-host-key" && r.Method == http.MethodGet:
		publicKey, fingerprint, err := sshHostKeyInfo(a.store.root)
		if err != nil {
			http.Error(w, "cannot load SSH host key", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"public_key": strings.TrimSpace(publicKey), "fingerprint": fingerprint})
		return
	case r.URL.Path == "/assets/trace-mark.svg" && r.Method == http.MethodGet:
		a.logo(w)
		return
	case r.URL.Path == "/assets/trace-network-hero.webp" && r.Method == http.MethodGet:
		a.heroImage(w)
		return
	case r.URL.Path == "/assets/inter.woff2" && r.Method == http.MethodGet:
		a.font(w)
		return
	case r.URL.Path == "/assets/space-grotesk.ttf" && r.Method == http.MethodGet:
		a.displayFont(w)
		return
	case r.URL.Path == "/assets/inter-license.txt" && r.Method == http.MethodGet:
		a.fontLicense(w)
		return
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		a.landing(w)
		return
	case r.URL.Path == "/login" && r.Method == http.MethodGet:
		a.loginPage(w, r, "", http.StatusOK)
		return
	case r.URL.Path == "/login" && r.Method == http.MethodPost:
		a.login(w, r)
		return
	case r.URL.Path == "/login/oidc" && r.Method == http.MethodGet:
		a.oidcLogin(w, r)
		return
	case r.URL.Path == "/login/oidc/callback" && r.Method == http.MethodGet:
		a.oidcCallback(w, r)
		return
	case r.URL.Path == "/logout" && r.Method == http.MethodPost:
		a.logout(w, r)
		return
	}
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/scim/v2/Users") || strings.HasPrefix(r.URL.Path, "/scim/v2/Groups") {
		a.scim(w, r, db)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1" {
		a.api(w, r, db)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/git/") {
		name, pass, _ := r.BasicAuth()
		u, valid := db.authenticate(name, pass)
		if valid {
			u = a.store.expandUser(name, u)
		}
		publicRead := false
		// Public, non-archived repositories serve upload-pack to everyone,
		// including signed-in users without a grant. Receive-pack never
		// qualifies, so writes always need an authenticated writer.
		if repo, ok := publicGitRepo(a.store, r.URL.Path); ok && anonymousGitRead(r) && (!valid || !u.canRead(repo)) {
			if !valid {
				name, u = "anonymous", userRecord{}
			}
			publicRead = true
		}
		if !valid && !publicRead {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trace Git"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		a.git(w, r, name, u, publicRead)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/lfs/") {
		name, pass, hasAuth := r.BasicAuth()
		u, valid := db.authenticate(name, pass)
		if valid {
			u = a.store.expandUser(name, u)
		}
		if !hasAuth || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trace LFS"`)
			writeLFSJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
			return
		}
		a.lfs(w, r, name, u)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/packages/") {
		name, pass, hasAuth := r.BasicAuth()
		u, valid := db.authenticate(name, pass)
		if valid {
			u = a.store.expandUser(name, u)
		}
		if !hasAuth || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trace packages"`)
			apiError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		a.packageHTTP(w, r, name, u)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/npm/") {
		name, pass, hasAuth := r.BasicAuth()
		u, valid := db.authenticate(name, pass)
		if valid {
			u = a.store.expandUser(name, u)
		}
		if !hasAuth || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trace npm"`)
			apiError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		a.npmHTTP(w, r, name, u)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/pypi/") {
		name, pass, hasAuth := r.BasicAuth()
		u, valid := db.authenticate(name, pass)
		if valid {
			u = a.store.expandUser(name, u)
		}
		if !hasAuth || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trace PyPI"`)
			apiError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		a.pypiHTTP(w, r, name, u)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/pages/") {
		a.pagesHTTP(w, r, db)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/raw/") {
		a.rawHTTP(w, r, db)
		return
	}
	name, u, valid := a.webIdentity(r, db)
	if !valid && r.Method == http.MethodGet {
		if publicName, ok := publicWebRepo(a.store, r.URL.Path); ok {
			name, valid = publicName, true
			u = userRecord{Repos: map[string]string{publicName: "read"}}
		}
	}
	if !valid {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	switch {
	case r.URL.Path == "/app" && r.Method == http.MethodGet:
		a.index(w, name, u, db)
	case r.URL.Path == "/search" && r.Method == http.MethodGet:
		a.globalSearchPage(w, r, u)
	case r.URL.Path == "/settings/federation" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.federationPage(w, r, name, u)
	case r.URL.Path == "/settings/teams" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.teamsPage(w, r, name, u)
	case (r.URL.Path == "/app" || r.URL.Path == "/repos" || r.URL.Path == "/actions/create-repo") && r.Method == http.MethodPost:
		a.create(w, r, u)
	case r.URL.Path == "/users" && r.Method == http.MethodPost && u.Admin:
		a.createUser(w, r)
	case r.URL.Path == "/users/rotate" && r.Method == http.MethodPost && u.Admin:
		a.rotateUser(w, r)
	case r.URL.Path == "/users/remove" && r.Method == http.MethodPost && u.Admin:
		a.removeUser(w, r)
	case r.URL.Path == "/access" && r.Method == http.MethodPost && u.Admin:
		a.grantAccess(w, r)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/search") && r.Method == http.MethodGet:
		a.searchPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/commits/") && r.Method == http.MethodGet:
		a.commitPage(w, r, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/settings/policy") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.policyPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/agents/import") && r.Method == http.MethodPost:
		a.agentBundleImportPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/agents/publish-git") && r.Method == http.MethodPost:
		a.agentGitPublishPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/agents") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.agentPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/packages") && r.Method == http.MethodGet:
		a.packagesPage(w, r, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/pulls/") && r.Method == http.MethodGet:
		a.pullPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/actions") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.actionPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/releases") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.releasePage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/star") && r.Method == http.MethodPost:
		a.starPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/watch") && r.Method == http.MethodPost:
		a.watchPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/projects") && r.Method == http.MethodGet:
		a.projectsPage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/projects") && r.Method == http.MethodPost:
		a.projectsAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/topics") && r.Method == http.MethodPost:
		a.topicsPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/") && strings.HasSuffix(r.URL.Path, "/assets") && r.Method == http.MethodPost:
		a.releaseAssetPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/") && strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/assets/") && r.Method == http.MethodGet:
		a.releaseAssetPageDownload(w, r, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/secrets") && r.Method == http.MethodPost:
		a.secretPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/fork") && r.Method == http.MethodPost:
		a.forkPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && (strings.HasSuffix(r.URL.Path, "/archive") || strings.HasSuffix(r.URL.Path, "/restore")) && r.Method == http.MethodPost:
		a.archivePageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && (strings.HasSuffix(r.URL.Path, "/delete") || strings.HasSuffix(r.URL.Path, "/transfer")) && r.Method == http.MethodPost:
		a.repoLifecycleAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/branches") && r.Method == http.MethodPost:
		a.branchPageAction(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && r.Method == http.MethodPost:
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/repos/"), "/issues") {
			a.issueAction(w, r, name, u)
		} else {
			a.repoAction(w, r, name, u)
		}
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/issues") && r.Method == http.MethodGet:
		a.issuePage(w, r, name, u)
	case strings.HasPrefix(r.URL.Path, "/repos/") && r.Method == http.MethodGet:
		a.repoPage(w, r, u, name)
	default:
		http.NotFound(w, r)
	}
}

func publicWebRepo(s *store, requestPath string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(requestPath, "/repos/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	name := parts[0] + "/" + parts[1]
	path, err := s.repoPath(name)
	return name, err == nil && isPublic(path) && !isArchived(path)
}

// anonymousGitRead reports whether r is one of the two smart-HTTP requests a
// read-only clone or fetch needs: the upload-pack ref advertisement (GET
// info/refs?service=git-upload-pack) and the upload-pack negotiation (POST
// git-upload-pack). Every receive-pack request stays authenticated.
func anonymousGitRead(r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs"):
		return r.URL.Query().Get("service") == "git-upload-pack"
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
		return true
	default:
		return false
	}
}

func publicGitRepo(s *store, requestPath string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(requestPath, "/git/"), "/")
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		return "", false
	}
	name := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if !validRepoName(name) {
		return "", false
	}
	path, err := s.repoPath(name)
	return name, err == nil && isPublic(path) && !isArchived(path)
}

func (a *app) git(w http.ResponseWriter, r *http.Request, username string, u userRecord, publicRead bool) {
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
	if !publicRead && !u.canRead(name) {
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
	service := r.URL.Query().Get("service")
	if suffix == "info/refs" && service != "git-upload-pack" && service != "git-receive-pack" {
		http.Error(w, "unsupported Git service", http.StatusBadRequest)
		return
	}
	write := suffix == "git-receive-pack" || service == "git-receive-pack"
	if write && (publicRead || !u.canWrite(name)) {
		http.Error(w, "write access required", http.StatusForbidden)
		return
	}
	path, _ := a.store.repoPath(name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if write && isMirror(path) {
		http.Error(w, "mirror is read-only", http.StatusForbidden)
		return
	}
	if write && isArchived(path) {
		http.Error(w, "repository is archived", http.StatusForbidden)
		return
	}
	if write && !hasManagedHook(path) {
		http.Error(w, "branch protection is unavailable", http.StatusServiceUnavailable)
		return
	}
	copyReq := r.Clone(r.Context())
	copyReq.Header.Del("Authorization")
	admin := "0"
	if u.Admin {
		admin = "1"
	}
	h := &cgi.Handler{Path: a.gitPath, Args: []string{"http-backend"}, Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + a.store.repos, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + username, "TRACE_ADMIN=" + admin}, Stderr: io.Discard}
	var beforeRefs map[string]string
	if write {
		beforeRefs, _ = a.store.repositoryRefs(name)
	}
	h.ServeHTTP(w, copyReq)
	if write {
		a.triggerActionsForChangedRefs(name, username, beforeRefs)
	}
}

func (a *app) triggerActionsForChangedRefs(repo, actor string, before map[string]string) {
	after, err := a.store.repositoryRefs(repo)
	if err != nil {
		return
	}
	for ref, commit := range after {
		if !strings.HasPrefix(ref, "refs/heads/") || before[ref] == commit {
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		// Keep the optional persistent search index current for changed branches.
		_, _ = a.store.rebuildSearchIndex(repo, branch)
		notifyWatchers(a.store, repo, actor, "repository.push", branch, actor+" pushed "+branch)
		if _, err := a.store.actionRun(repo, branch, actor); err == nil {
			_ = a.store.recordAudit(actor, "action.auto_run", repo, branch, map[string]any{"commit": commit})
		}
	}
}
