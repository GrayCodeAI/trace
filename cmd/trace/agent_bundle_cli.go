package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func agentCommand(args []string) error {
	if len(args) > 0 && args[0] == "git" {
		return agentGitCommand(args)
	}
	if len(args) > 0 && args[0] != "bundle" {
		return agentCaptureCommand(args)
	}
	if len(args) < 2 || args[0] != "bundle" || (args[1] != "export" && args[1] != "import") {
		return errors.New("usage: trace agent <bundle|git|configure|codex-notify|claude-stop|gemini-after-agent|cursor-stop|sync|pending|drop> ...")
	}
	fs := flag.NewFlagSet("agent bundle "+args[1], flag.ContinueOnError)
	data := fs.String("data", "./data", "Trace data directory")
	output := fs.String("out", "", "new private JSON file for export")
	input := fs.String("file", "", "signed JSON bundle to import")
	expected := fs.String("expected-node-id", "", "trusted source node ID required for import")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
		return errors.New("usage: trace agent bundle <export -out FILE|import -file FILE -expected-node-id ID> [-data DIR] OWNER/NAME")
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	repo := fs.Arg(0)
	if args[1] == "export" {
		if *output == "" || *input != "" || *expected != "" {
			return errors.New("usage: trace agent bundle export [-data DIR] -out NEW_FILE OWNER/NAME")
		}
		bundle, err := s.exportAgentBundle(repo)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			_ = os.Remove(*output)
			return err
		}
		if err := file.Sync(); err != nil {
			_ = os.Remove(*output)
			return err
		}
		fmt.Println("exported signed agent bundle to", *output)
		return nil
	}
	if *input == "" || *output != "" || !validFederationNodeID(*expected) {
		return errors.New("usage: trace agent bundle import [-data DIR] -file FILE -expected-node-id ID OWNER/NAME")
	}
	file, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxAgentBundleBytes+64<<10+1))
	if err != nil {
		return err
	}
	if len(encoded) > maxAgentBundleBytes+64<<10 {
		return errors.New("agent bundle file exceeds size limit")
	}
	var bundle signedAgentBundle
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		return fmt.Errorf("invalid agent bundle JSON: %w", err)
	}
	result, err := s.importAgentBundle(repo, *expected, bundle)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d sessions and %d checkpoints into %s\n", result.SessionsCreated, result.CheckpointsAdded, repo)
	return nil
}
