package main

import (
	"errors"
	"flag"
	"fmt"
)

func agentGitCommand(args []string) error {
	if len(args) < 2 || args[0] != "git" {
		return errors.New("usage: trace agent git <publish|import|unpublish> ...")
	}
	fs := flag.NewFlagSet("agent git "+args[1], flag.ContinueOnError)
	data := fs.String("data", "./data", "Trace data directory")
	expected := fs.String("expected-node-id", "", "verified source node ID for import or own node ID for unpublish")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
		return errors.New("usage: trace agent git <publish|import -expected-node-id ID|unpublish -expected-node-id ID> [-data DIR] OWNER/NAME")
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	repo := fs.Arg(0)
	switch args[1] {
	case "publish":
		if *expected != "" {
			return errors.New("-expected-node-id is not used for publish")
		}
		result, err := s.publishAgentGitBundle(repo)
		if err != nil {
			return err
		}
		if result.Changed {
			fmt.Printf("published %d signed agent sessions at %s (%s)\n", result.Sessions, result.Ref, result.Commit)
		} else {
			fmt.Printf("Git agent history already current at %s (%s)\n", result.Ref, result.Commit)
		}
		fmt.Println("source node ID:", result.NodeID)
		return nil
	case "import":
		if !validFederationNodeID(*expected) {
			return errors.New("usage: trace agent git import -expected-node-id VERIFIED_ID [-data DIR] OWNER/NAME")
		}
		result, err := s.importAgentGitBundle(repo, *expected)
		if err != nil {
			return err
		}
		fmt.Printf("imported %d sessions and %d checkpoints from Git into %s\n", result.SessionsCreated, result.CheckpointsAdded, repo)
		return nil
	case "unpublish":
		if !validFederationNodeID(*expected) {
			return errors.New("usage: trace agent git unpublish -expected-node-id OWN_ID [-data DIR] OWNER/NAME")
		}
		result, err := s.unpublishAgentGitBundle(repo, *expected)
		if err != nil {
			return err
		}
		fmt.Printf("removed %s; old Git objects and previously fetched copies may still exist\n", result.Ref)
		return nil
	default:
		return errors.New("usage: trace agent git <publish|import|unpublish> ...")
	}
}
