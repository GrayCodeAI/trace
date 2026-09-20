package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

const federationPeersFile = "federation-peers.json"

type federationPeer struct {
	Repo      string `json:"repo"`
	Source    string `json:"source"`
	Username  string `json:"username"`
	TokenFile string `json:"token_file"`
}

func (s *store) loadFederationPeers() ([]federationPeer, error) {
	b, err := os.ReadFile(filepath.Join(s.root, federationPeersFile))
	if errors.Is(err, os.ErrNotExist) {
		return []federationPeer{}, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []federationPeer
	if err := json.Unmarshal(b, &peers); err != nil {
		return nil, fmt.Errorf("read federation peers: %w", err)
	}
	return peers, nil
}

func (s *store) saveFederationPeers(peers []federationPeer) error {
	sort.Slice(peers, func(i, j int) bool { return peers[i].Repo < peers[j].Repo })
	b, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".federation-peers-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.root, federationPeersFile))
}

func (s *store) addFederationPeer(peer federationPeer) error {
	if !validRepoName(peer.Repo) || peer.Source == "" || !namePattern.MatchString(peer.Username) || peer.TokenFile == "" {
		return errors.New("invalid federation peer")
	}
	if _, err := os.Stat(peer.TokenFile); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	u, err := url.Parse(peer.Source)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return errors.New("source URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	peers, err := s.loadFederationPeers()
	if err != nil {
		return err
	}
	for i := range peers {
		if peers[i].Repo == peer.Repo {
			peers[i] = peer
			return s.saveFederationPeers(peers)
		}
	}
	return s.saveFederationPeers(append(peers, peer))
}

func (s *store) removeFederationPeer(repo string) error {
	if !validRepoName(repo) {
		return errors.New("invalid repository")
	}
	peers, err := s.loadFederationPeers()
	if err != nil {
		return err
	}
	filtered := peers[:0]
	removed := false
	for _, peer := range peers {
		if peer.Repo == repo {
			removed = true
			continue
		}
		filtered = append(filtered, peer)
	}
	if !removed {
		return errors.New("federation peer not found")
	}
	return s.saveFederationPeers(filtered)
}

func (s *store) syncFederationPeers(repo string) error {
	peers, err := s.loadFederationPeers()
	if err != nil {
		return err
	}
	matched := 0
	for _, peer := range peers {
		if repo != "" && peer.Repo != repo {
			continue
		}
		matched++
		if err := s.syncMirrorWithManifest(peer.Repo, peer.Source, peer.Username, peer.TokenFile, true); err != nil {
			return fmt.Errorf("sync %s: %w", peer.Repo, err)
		}
	}
	if matched == 0 {
		return errors.New("no federation peers configured")
	}
	return nil
}

func federationPeerCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace federation peer <add|list|remove|sync> ...")
	}
	fs := flag.NewFlagSet("federation peer "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	from := fs.String("from", "", "source Trace Git URL")
	user := fs.String("user", "admin", "source username")
	tokenFile := fs.String("token-file", "", "source token file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *from == "" || *tokenFile == "" {
			return errors.New("usage: trace federation peer add [-data DIR] -from URL -user USER -token-file FILE OWNER/NAME")
		}
		if err := s.addFederationPeer(federationPeer{Repo: fs.Arg(0), Source: *from, Username: *user, TokenFile: *tokenFile}); err != nil {
			return err
		}
		fmt.Println("saved federation peer", fs.Arg(0))
		return nil
	case "list":
		if fs.NArg() != 0 {
			return errors.New("usage: trace federation peer list [-data DIR]")
		}
		peers, err := s.loadFederationPeers()
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(peers, "", "  ")
		fmt.Println(string(b))
		return nil
	case "remove":
		if fs.NArg() != 1 {
			return errors.New("usage: trace federation peer remove [-data DIR] OWNER/NAME")
		}
		return s.removeFederationPeer(fs.Arg(0))
	case "sync":
		if fs.NArg() > 1 || (fs.NArg() == 1 && !validRepoName(fs.Arg(0))) {
			return errors.New("usage: trace federation peer sync [-data DIR] [OWNER/NAME]")
		}
		repo := ""
		if fs.NArg() == 1 {
			repo = fs.Arg(0)
		}
		return s.syncFederationPeers(repo)
	default:
		return errors.New("usage: trace federation peer <add|list|remove|sync> ...")
	}
}
