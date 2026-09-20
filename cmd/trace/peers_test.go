package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFederationPeerConfigurationPersists(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s := &store{root: root, repos: filepath.Join(root, "repos")}
	tokenPath := filepath.Join(root, "source-token")
	if err := os.WriteFile(tokenPath, []byte("token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	peer := federationPeer{Repo: "alice/demo", Source: "http://127.0.0.1:8787/git/alice/demo.git", Username: "mirror", TokenFile: tokenPath}
	if err := s.addFederationPeer(peer); err != nil {
		t.Fatal(err)
	}
	peers, err := s.loadFederationPeers()
	if err != nil || len(peers) != 1 || peers[0] != peer {
		t.Fatalf("persisted peers = %#v, err=%v", peers, err)
	}
	peer.Source = "https://trace.example/git/alice/demo.git"
	if err := s.addFederationPeer(peer); err != nil {
		t.Fatal(err)
	}
	peers, err = s.loadFederationPeers()
	if err != nil || len(peers) != 1 || peers[0].Source != peer.Source {
		t.Fatalf("updated peers = %#v, err=%v", peers, err)
	}
	if err := s.removeFederationPeer(peer.Repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, federationPeersFile)); err != nil {
		t.Fatal(err)
	}
}
