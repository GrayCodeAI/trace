package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

type userRecord struct {
	Hash        string            `json:"hash"`
	Admin       bool              `json:"admin,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	Repos       map[string]string `json:"repos,omitempty"`
	SSHKeys     []string          `json:"ssh_keys,omitempty"`
	TOTPSecret  string            `json:"totp_secret,omitempty"`
	TOTPEnabled bool              `json:"totp_enabled,omitempty"`
	// OIDCIssuer and OIDCSubject bind the account to one identity-provider
	// identity. OIDC sign-in only ever opens the account whose stored
	// (issuer, subject) pair matches; usernames and emails are not trusted.
	OIDCIssuer  string `json:"oidc_issuer,omitempty"`
	OIDCSubject string `json:"oidc_subject,omitempty"`
}

func sshKeyCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace user key <add|remove> [-data DIR] USER [AUTHORIZED_KEY|FINGERPRINT]")
	}
	fs := flag.NewFlagSet("user key "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: trace user key <add|remove> [-data DIR] USER [AUTHORIZED_KEY|FINGERPRINT]")
	}
	user, value := fs.Arg(0), fs.Arg(1)
	switch args[0] {
	case "add":
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(value))
		if err != nil {
			return fmt.Errorf("parse public key: %w", err)
		}
		canonical := string(ssh.MarshalAuthorizedKey(key))
		return s.updateUsers(func(db *userDB) error {
			u, ok := db.Users[user]
			if !ok {
				return errors.New("user not found")
			}
			for _, existing := range u.SSHKeys {
				candidate, _, _, _, parseErr := ssh.ParseAuthorizedKey([]byte(existing))
				if parseErr == nil && sshKeysEqual(candidate, key) {
					return errors.New("SSH key already exists")
				}
			}
			u.SSHKeys = append(u.SSHKeys, canonical)
			db.Users[user] = u
			return nil
		})
	case "remove":
		return s.updateUsers(func(db *userDB) error {
			u, ok := db.Users[user]
			if !ok {
				return errors.New("user not found")
			}
			filtered := u.SSHKeys[:0]
			removed := false
			for _, existing := range u.SSHKeys {
				key, _, _, _, parseErr := ssh.ParseAuthorizedKey([]byte(existing))
				if parseErr == nil && (existing == value || ssh.FingerprintSHA256(key) == value) {
					removed = true
					continue
				}
				filtered = append(filtered, existing)
			}
			if !removed {
				return errors.New("SSH key not found")
			}
			u.SSHKeys = filtered
			db.Users[user] = u
			return nil
		})
	default:
		return errors.New("usage: trace user key <add|remove> [-data DIR] USER [AUTHORIZED_KEY|FINGERPRINT]")
	}
}

type userDB struct {
	Users map[string]userRecord `json:"users"`
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *store) loadUsers() (userDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "users.json"))
	if errors.Is(err, os.ErrNotExist) {
		// Existing single-user nodes keep their original admin token.
		legacy, legacyErr := os.ReadFile(filepath.Join(s.root, tokenFilename))
		if legacyErr != nil {
			return userDB{}, legacyErr
		}
		token := strings.TrimSpace(string(legacy))
		if token == "" {
			return userDB{}, errors.New("admin token file is empty")
		}
		return userDB{Users: map[string]userRecord{"admin": {Hash: hashToken(token), Admin: true}}}, nil
	}
	if err != nil {
		return userDB{}, err
	}
	var db userDB
	if err := json.Unmarshal(b, &db); err != nil {
		return userDB{}, fmt.Errorf("read users: %w", err)
	}
	if len(db.Users) == 0 {
		return userDB{}, errors.New("no users configured")
	}
	return db, nil
}

func (s *store) updateUsers(change func(*userDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".users.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadUsers()
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
	tmp, err := os.CreateTemp(s.root, ".users-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.root, "users.json"))
}

func (s *store) addUser(name string, admin bool) (string, error) {
	if !namePattern.MatchString(name) || name == "git" {
		return "", errors.New("invalid username")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.updateUsers(func(db *userDB) error {
		if _, exists := db.Users[name]; exists {
			return errors.New("user already exists")
		}
		db.Users[name] = userRecord{Hash: hashToken(token), Admin: admin}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *store) rotateUser(name string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.updateUsers(func(db *userDB) error {
		u, exists := db.Users[name]
		if !exists {
			return errors.New("user not found")
		}
		u.Hash = hashToken(token)
		db.Users[name] = u
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *store) removeUser(name string) error {
	return s.updateUsers(func(db *userDB) error {
		u, exists := db.Users[name]
		if !exists {
			return errors.New("user not found")
		}
		if u.Admin {
			admins := 0
			for _, candidate := range db.Users {
				if candidate.Admin {
					admins++
				}
			}
			if admins == 1 {
				return errors.New("cannot remove the last admin")
			}
		}
		delete(db.Users, name)
		return nil
	})
}

func (s *store) grantUser(name, repo, role string) error {
	if role != "read" && role != "write" && role != "maintain" && role != "none" {
		return errors.New("role must be read, write, maintain, or none")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("repository not found: %w", err)
	}
	return s.updateUsers(func(db *userDB) error {
		u, exists := db.Users[name]
		if !exists {
			return errors.New("user not found")
		}
		if u.Repos == nil {
			u.Repos = make(map[string]string)
		}
		if role == "none" {
			delete(u.Repos, repo)
		} else {
			u.Repos[repo] = role
		}
		db.Users[name] = u
		return nil
	})
}

func (db userDB) authenticate(name, token string) (userRecord, bool) {
	u, exists := db.Users[name]
	if !exists {
		return userRecord{}, false
	}
	if u.Disabled {
		return userRecord{}, false
	}
	want, err := hex.DecodeString(u.Hash)
	if err != nil || len(want) != sha256.Size {
		return userRecord{}, false
	}
	got := sha256.Sum256([]byte(token))
	return u, subtle.ConstantTimeCompare(got[:], want) == 1
}

func (s *store) setUserActive(name string, active bool) error {
	return s.updateUsers(func(db *userDB) error {
		u, ok := db.Users[name]
		if !ok {
			return errors.New("user not found")
		}
		u.Disabled = !active
		db.Users[name] = u
		return nil
	})
}

func (u userRecord) canRead(repo string) bool {
	return u.Admin || u.Repos[repo] == "read" || u.Repos[repo] == "write" || u.Repos[repo] == "maintain"
}

func (u userRecord) canWrite(repo string) bool {
	return u.Admin || u.Repos[repo] == "write" || u.Repos[repo] == "maintain"
}

func (u userRecord) canMaintain(repo string) bool {
	return u.Admin || u.Repos[repo] == "maintain"
}

func userCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace user <add|grant|rotate|remove|list> [-data DIR] ...")
	}
	if args[0] == "key" {
		return sshKeyCommand(args[1:])
	}
	if args[0] == "2fa" {
		return totpCommand(args[1:])
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	admin := fs.Bool("admin", false, "create an administrator (add only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if fs.NArg() != 1 {
			return errors.New("usage: trace user add [-data DIR] [-admin] USER")
		}
		token, err := s.addUser(fs.Arg(0), *admin)
		if err != nil {
			return err
		}
		fmt.Printf("created %s\nToken: %s\nShow this token to the user once over a private channel.\n", fs.Arg(0), token)
	case "grant":
		if fs.NArg() != 3 {
			return errors.New("usage: trace user grant [-data DIR] USER OWNER/REPO <read|write|none>")
		}
		if err := s.grantUser(fs.Arg(0), fs.Arg(1), fs.Arg(2)); err != nil {
			return err
		}
		fmt.Printf("%s: %s on %s\n", fs.Arg(0), fs.Arg(2), fs.Arg(1))
	case "rotate":
		if fs.NArg() != 1 {
			return errors.New("usage: trace user rotate [-data DIR] USER")
		}
		token, err := s.rotateUser(fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Printf("rotated %s\nToken: %s\n", fs.Arg(0), token)
	case "remove":
		if fs.NArg() != 1 {
			return errors.New("usage: trace user remove [-data DIR] USER")
		}
		if err := s.removeUser(fs.Arg(0)); err != nil {
			return err
		}
		fmt.Println("removed", fs.Arg(0))
	case "list":
		if fs.NArg() != 0 {
			return errors.New("usage: trace user list [-data DIR]")
		}
		db, err := s.loadUsers()
		if err != nil {
			return err
		}
		var names []string
		for name := range db.Users {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			u := db.Users[name]
			role := "member"
			if u.Admin {
				role = "admin"
			}
			fmt.Printf("%s (%s)\n", name, role)
		}
	default:
		return errors.New("usage: trace user <add|grant|rotate|remove|list> [-data DIR] ...")
	}
	return nil
}
