package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const federationTrustFile = "federation-trust.json"

type federationTrust struct {
	Repo     string    `json:"repo"`
	Source   string    `json:"source"`
	NodeID   string    `json:"node_id"`
	PinnedAt time.Time `json:"pinned_at"`
}

func validFederationNodeID(nodeID string) bool {
	publicKey, err := base64.RawURLEncoding.DecodeString(nodeID)
	return err == nil && len(publicKey) == ed25519.PublicKeySize && base64.RawURLEncoding.EncodeToString(publicKey) == nodeID
}

func (s *store) loadFederationTrust() ([]federationTrust, error) {
	b, err := os.ReadFile(filepath.Join(s.root, federationTrustFile))
	if errors.Is(err, os.ErrNotExist) {
		return []federationTrust{}, nil
	}
	if err != nil {
		return nil, err
	}
	var pins []federationTrust
	if err := json.Unmarshal(b, &pins); err != nil {
		return nil, fmt.Errorf("read federation trust: %w", err)
	}
	seen := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		if !validRepoName(pin.Repo) || pin.Source == "" || !validFederationNodeID(pin.NodeID) {
			return nil, errors.New("invalid federation trust record")
		}
		key := pin.Repo + "\x00" + pin.Source
		if _, exists := seen[key]; exists {
			return nil, errors.New("duplicate federation trust record")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(pins, func(i, j int) bool {
		if pins[i].Repo == pins[j].Repo {
			return pins[i].Source < pins[j].Source
		}
		return pins[i].Repo < pins[j].Repo
	})
	return pins, nil
}

func (s *store) saveFederationTrust(pins []federationTrust) error {
	b, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".federation-trust-")
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
	return os.Rename(name, filepath.Join(s.root, federationTrustFile))
}

func (s *store) updateFederationTrust(change func(*[]federationTrust) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".federation-trust.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	pins, err := s.loadFederationTrust()
	if err != nil {
		return err
	}
	if err := change(&pins); err != nil {
		return err
	}
	return s.saveFederationTrust(pins)
}

func (s *store) checkOrPinFederationIdentity(repo, source, nodeID string) error {
	if !validRepoName(repo) || source == "" || !validFederationNodeID(nodeID) {
		return errors.New("invalid federation source identity")
	}
	return s.updateFederationTrust(func(pins *[]federationTrust) error {
		for _, pin := range *pins {
			if pin.Repo == repo && pin.Source == source {
				if pin.NodeID != nodeID {
					return fmt.Errorf("federation source identity changed for %s; synchronization stopped", repo)
				}
				return nil
			}
		}
		*pins = append(*pins, federationTrust{Repo: repo, Source: source, NodeID: nodeID, PinnedAt: time.Now().UTC()})
		return nil
	})
}

func (s *store) forgetFederationIdentity(repo, source, expectedNodeID string) error {
	if !validRepoName(repo) || source == "" || !validFederationNodeID(expectedNodeID) {
		return errors.New("invalid federation trust removal")
	}
	return s.updateFederationTrust(func(pins *[]federationTrust) error {
		for i, pin := range *pins {
			if pin.Repo == repo && pin.Source == source {
				if pin.NodeID != expectedNodeID {
					return errors.New("pinned node identity does not match")
				}
				*pins = append((*pins)[:i], (*pins)[i+1:]...)
				return nil
			}
		}
		return errors.New("federation source identity is not pinned")
	})
}
