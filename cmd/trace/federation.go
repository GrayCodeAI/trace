package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type repositoryManifest struct {
	Repo      string            `json:"repo"`
	NodeID    string            `json:"node_id"`
	CreatedAt time.Time         `json:"created_at"`
	Refs      map[string]string `json:"refs"`
}

type signedRepositoryManifest struct {
	Payload   repositoryManifest `json:"payload"`
	PublicKey string             `json:"public_key"`
	Signature string             `json:"signature"`
}

const nodeIdentityPath = "node/identity_ed25519"

func loadOrCreateNodeSigner(root string) (ed25519.PrivateKey, error) {
	path := filepath.Join(root, nodeIdentityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".identity.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			return nil, err
		}
		b = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		if err := tmp.Chmod(0600); err != nil {
			tmp.Close()
			return nil, err
		}
		if _, err := tmp.Write(b); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("invalid node identity PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("node identity is not Ed25519")
	}
	return private, nil
}

func (s *store) repositoryManifest(repo string) (signedRepositoryManifest, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return signedRepositoryManifest{}, errors.New("repository not found")
	}
	refs, err := s.repositoryRefs(repo)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	// Force stable map materialization before signing so future encoders cannot
	// accidentally sign a different representation.
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(refs))
	for _, key := range keys {
		ordered[key] = refs[key]
	}
	private, err := loadOrCreateNodeSigner(s.root)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	payload := repositoryManifest{Repo: repo, NodeID: base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey)), CreatedAt: time.Now().UTC().Truncate(time.Second), Refs: ordered}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	return signedRepositoryManifest{Payload: payload, PublicKey: base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey)), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, encoded))}, nil
}

func (s *store) repositoryRefs(repo string) (map[string]string, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return nil, err
	}
	output, truncated, err := gitOutput(path, 1<<20, "for-each-ref", "--format=%(refname)\t%(objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, errors.New("repository ref listing exceeds 1 MiB")
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || (!strings.HasPrefix(fields[0], "refs/heads/") && !strings.HasPrefix(fields[0], "refs/tags/")) || !validGitObjectID(fields[1]) {
			return nil, errors.New("invalid repository ref listing")
		}
		refs[fields[0]] = fields[1]
	}
	return refs, nil
}

func fetchRemoteManifest(source, repo, username, token string) (signedRepositoryManifest, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return signedRepositoryManifest{}, errors.New("invalid source URL")
	}
	endpoint := u.Scheme + "://" + u.Host + "/api/v1/federation/repos/" + url.PathEscape(strings.Split(repo, "/")[0]) + "/" + url.PathEscape(strings.Split(repo, "/")[1]) + "/manifest"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	req.SetBasicAuth(username, token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return signedRepositoryManifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return signedRepositoryManifest{}, fmt.Errorf("source returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var manifest signedRepositoryManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&manifest); err != nil {
		return signedRepositoryManifest{}, err
	}
	return manifest, nil
}

func verifyRepositoryManifest(manifest signedRepositoryManifest) error {
	publicBytes, err := base64.RawURLEncoding.DecodeString(manifest.PublicKey)
	if err != nil || len(publicBytes) != ed25519.PublicKeySize {
		return errors.New("invalid manifest public key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid manifest signature")
	}
	payload, err := json.Marshal(manifest.Payload)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicBytes), payload, signature) {
		return errors.New("manifest signature verification failed")
	}
	expectedNodeID := base64.RawURLEncoding.EncodeToString(publicBytes)
	if manifest.Payload.NodeID != expectedNodeID {
		return errors.New("manifest node id does not match its public key")
	}
	return nil
}

func formatManifest(manifest signedRepositoryManifest) (string, error) {
	b, err := json.MarshalIndent(manifest, "", "  ")
	return string(b), err
}
