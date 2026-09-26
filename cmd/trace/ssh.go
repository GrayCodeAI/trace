package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

func sshKeysEqual(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

const sshHostKeyFile = "ssh/host_ed25519"

var gitSSHCommandPattern = regexp.MustCompile(`^(git-(?:upload|receive)-pack) '([^']+)'$`)

func loadOrCreateSSHHostSigner(root string) (ssh.Signer, error) {
	path := filepath.Join(root, sshHostKeyFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509MarshalPKCS8(private)
		if err != nil {
			return nil, err
		}
		b = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	key, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse SSH host key: %w", err)
	}
	return key, nil
}

func sshHostKeyInfo(root string) (string, string, error) {
	signer, err := loadOrCreateSSHHostSigner(root)
	if err != nil {
		return "", "", err
	}
	key := signer.PublicKey()
	return string(ssh.MarshalAuthorizedKey(key)), ssh.FingerprintSHA256(key), nil
}

// rotateSSHHostKey replaces the persisted host key atomically. Existing SSH
// listeners keep their in-memory signer; restart Trace after rotation so new
// connections advertise the new fingerprint.
func rotateSSHHostKey(root string) (string, string, error) {
	path := filepath.Join(root, sshHostKeyFile)
	lock, err := os.OpenFile(filepath.Join(root, "ssh", ".host-key.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509MarshalPKCS8(private)
	if err != nil {
		return "", "", err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".host-ed25519-")
	if err != nil {
		return "", "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0600)
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err = tmp.Close(); err != nil {
		return "", "", err
	}
	if err = os.Rename(name, path); err != nil {
		return "", "", err
	}
	return sshHostKeyInfo(root)
}

func x509MarshalPKCS8(key ed25519.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(key)
}

func serveSSH(s *store, addr string) error {
	config, err := sshServerConfig(s)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("trace SSH listening on %s", addr)
	return serveSSHListener(s, config, listener)
}

func sshServerConfig(s *store) (*ssh.ServerConfig, error) {
	signer, err := loadOrCreateSSHHostSigner(s.root)
	if err != nil {
		return nil, err
	}
	config := &ssh.ServerConfig{}
	config.AddHostKey(signer)
	config.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		db, err := s.loadUsers()
		if err != nil {
			return nil, err
		}
		for username, user := range db.Users {
			for _, encoded := range user.SSHKeys {
				candidate, _, _, _, err := ssh.ParseAuthorizedKey([]byte(encoded))
				if err == nil && sshKeysEqual(candidate, key) {
					return &ssh.Permissions{Extensions: map[string]string{"trace-user": username}}, nil
				}
			}
		}
		return nil, errors.New("SSH key is not authorized")
	}
	return config, nil
}

func serveSSHListener(s *store, config *ssh.ServerConfig, listener net.Listener) error {
	limiter := newRateLimiter(s.root)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		if decision := limiter.allowSSH(conn.RemoteAddr().String()); !decision.Allowed {
			_ = conn.Close()
			continue
		}
		go handleSSHConnection(s, config, conn)
	}
}

func handleSSHConnection(s *store, config *ssh.ServerConfig, conn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for channel := range chans {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.UnknownChannelType, "sessions only")
			continue
		}
		ch, requests, err := channel.Accept()
		if err != nil {
			continue
		}
		go handleSSHSession(s, sshConn.Permissions, ch, requests)
	}
}

func handleSSHSession(s *store, permissions *ssh.Permissions, ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	var command string
	for req := range requests {
		switch req.Type {
		case "exec":
			if len(req.Payload) < 4 {
				_ = req.Reply(false, nil)
				return
			}
			command = string(req.Payload[4:])
			_ = req.Reply(true, nil)
		case "env":
			_ = req.Reply(false, nil)
		default:
			_ = req.Reply(false, nil)
		}
		if command != "" {
			break
		}
	}
	if command == "" || permissions == nil {
		return
	}
	matches := gitSSHCommandPattern.FindStringSubmatch(command)
	if len(matches) != 3 {
		_, _ = io.WriteString(ch.Stderr(), "Trace: unsupported SSH command\n")
		return
	}
	service, name := matches[1], strings.TrimSuffix(strings.TrimPrefix(matches[2], "/"), ".git")
	if !validRepoName(name) {
		_, _ = io.WriteString(ch.Stderr(), "Trace: invalid repository\n")
		return
	}
	db, err := s.loadUsers()
	if err != nil {
		return
	}
	username := permissions.Extensions["trace-user"]
	u, ok := db.Users[username]
	if ok {
		u = s.expandUser(username, u)
	}
	if !ok || !u.canRead(name) || (service == "git-receive-pack" && !u.canWrite(name)) {
		_, _ = io.WriteString(ch.Stderr(), "Trace: repository access denied\n")
		return
	}
	path, err := s.repoPath(name)
	if err != nil {
		_, _ = io.WriteString(ch.Stderr(), "Trace: repository is unavailable\n")
		return
	}
	if _, err := os.Stat(path); err != nil {
		_, _ = io.WriteString(ch.Stderr(), "Trace: repository is unavailable\n")
		return
	}
	// Mirror the HTTP receive-pack checks: mirrors and archived repositories
	// are read-only, and pushes need the managed protection hook.
	if service == "git-receive-pack" {
		refusal := ""
		switch {
		case isMirror(path):
			refusal = "Trace: mirror is read-only\n"
		case isArchived(path):
			refusal = "Trace: repository is archived\n"
		case !hasManagedHook(path):
			refusal = "Trace: branch protection is unavailable\n"
		}
		if refusal != "" {
			_, _ = io.WriteString(ch.Stderr(), refusal)
			return
		}
	}
	cmd := exec.Command(service, path)
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	cmd.Env = append(os.Environ(), "REMOTE_USER="+username)
	if u.Admin {
		cmd.Env = append(cmd.Env, "TRACE_ADMIN=1")
	}
	err = cmd.Run()
	status := uint32(0)
	if err != nil {
		status = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = uint32(exitErr.ExitCode())
		}
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, status)
	_, _ = ch.SendRequest("exit-status", false, payload)
}
