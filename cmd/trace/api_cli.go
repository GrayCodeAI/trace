package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func apiCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace api <repos|audit|branches|commits|pulls|issues|...> -url URL -user USER -token-file FILE [OWNER/NAME]")
	}
	fs := flag.NewFlagSet("api "+args[0], flag.ContinueOnError)
	base := fs.String("url", "http://127.0.0.1:8787", "Trace server URL")
	user := fs.String("user", "admin", "Trace username")
	tokenFile := fs.String("token-file", "", "file containing the personal token")
	limit := fs.Int("limit", 20, "commit count for commits")
	state := fs.String("state", "open", "pull-request state")
	title := fs.String("title", "", "pull-request title")
	description := fs.String("description", "", "pull-request description")
	source := fs.String("source", "", "pull-request source branch")
	target := fs.String("target", "main", "pull-request target branch")
	branchName := fs.String("branch", "", "branch name for branch-create")
	statusContext := fs.String("context", "", "commit status context")
	statusState := fs.String("status", "", "commit status state")
	statusURL := fs.String("status-url", "", "commit status target URL")
	ref := fs.String("ref", "main", "branch ref for an action run")
	query := fs.String("query", "", "literal code or session search query")
	scope := fs.String("scope", "all", "search scope: all, code, commits, or sessions")
	after := fs.String("after", "", "repository cursor for search-all pagination")
	tag := fs.String("tag", "", "release tag")
	draft := fs.Bool("draft", false, "create a draft release")
	public := fs.Bool("public", false, "make repository publicly readable")
	prerelease := fs.Bool("prerelease", false, "mark release as prerelease")
	dueDate := fs.String("due-date", "", "milestone due date (YYYY-MM-DD)")
	bodyText := fs.String("body", "", "pull-request comment body")
	commentFile := fs.String("file", "", "file path for a line-level pull-request comment")
	commentLine := fs.Int("line", 0, "line number for a line-level pull-request comment")
	commentSide := fs.String("side", "new", "line comment side (new)")
	mergeStrategy := fs.String("strategy", "ff", "pull-request merge strategy: ff, squash, or merge")
	pullAssignees := fs.String("assignees", "", "comma-separated pull-request assignees")
	pullReviewers := fs.String("reviewers", "", "comma-separated requested pull-request reviewers")
	mergeQueuePR := fs.Int("pull-request", 0, "pull-request ID for merge queue enqueue")
	agentName := fs.String("agent", "", "agent name for an agent session")
	checkpointState := fs.String("checkpoint-state", "", "checkpoint state label")
	requiredApprovals := fs.Int("required-approvals", 1, "required non-author approvals for a repository policy")
	requiredChecks := fs.String("required-checks", "", "comma-separated required successful action job names")
	protectedBranches := fs.String("protected-branches", "main", "comma-separated protected branch patterns; * may appear at the end")
	requireCodeOwners := fs.Bool("require-codeowners", false, "require an approval from a matching CODEOWNERS user or team for changed files")
	assignee := fs.String("assignee", "", "issue assignee")
	labels := fs.String("labels", "", "comma-separated issue labels")
	topics := fs.String("topics", "", "comma-separated repository topics")
	webhookURL := fs.String("webhook-url", "", "webhook HTTPS URL")
	webhookSecret := fs.String("webhook-secret", "", "webhook signing secret")
	webhookEvents := fs.String("events", "", "comma-separated webhook events")
	secretName := fs.String("secret", "", "CI secret name")
	secretValue := fs.String("secret-value", "", "CI secret value (prefer -secret-file)")
	secretFile := fs.String("secret-file", "", "file containing a CI secret value")
	pagesBranch := fs.String("pages-branch", "main", "Pages source branch")
	pagesRoot := fs.String("pages-root", "", "Pages source directory")
	rawPath := fs.String("path", "", "raw repository file path")
	rawOutput := fs.String("output", "", "write raw output to a file")
	assetName := fs.String("asset-name", "", "release asset filename")
	assetFile := fs.String("asset-file", "", "local file to upload as a release asset")
	peerSource := fs.String("peer-source", "", "source Trace Git URL for a federation peer")
	peerUser := fs.String("peer-user", "admin", "source username for a federation peer")
	peerTokenFile := fs.String("peer-token-file", "", "source token-file path for a federation peer")
	trustNodeID := fs.String("node-id", "", "currently pinned federation node ID")
	bundleFile := fs.String("bundle-file", "", "signed agent bundle JSON file")
	bundleExpectedNodeID := fs.String("expected-node-id", "", "trusted source node ID for agent bundle import")
	conflictRef := fs.String("conflict-ref", "", "full ref for a federation conflict")
	conflictLocal := fs.String("conflict-local", "", "current mirror object ID for a federation conflict")
	conflictRemote := fs.String("conflict-remote", "", "signed source object ID for a federation conflict")
	conflictDelete := fs.Bool("conflict-delete", false, "approve deletion of a ref absent from the signed source")
	projectName := fs.String("project", "", "project name")
	cardKind := fs.String("card-kind", "", "project card kind: issue or pull")
	cardNumber := fs.Int("card-number", 0, "issue or pull request number")
	cardColumn := fs.String("column", "", "project column")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *tokenFile == "" {
		return errors.New("-token-file is required; do not put tokens in command arguments")
	}
	tokenBytes, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("token file is empty")
	}
	endpoint, err := url.Parse(strings.TrimRight(*base, "/"))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return errors.New("-url must be an absolute HTTP(S) URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return errors.New("-url must use HTTP or HTTPS")
	}
	var path string
	method := http.MethodGet
	var body string
	contentType := ""
	switch args[0] {
	case "repos":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api repos -url URL -user USER -token-file FILE")
		}
		path = "/api/v1/repos"
	case "ssh-host-key", "ssh-host-key-rotate":
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE", args[0])
		}
		path = "/api/v1/ssh/host-key"
		if args[0] == "ssh-host-key-rotate" {
			method = http.MethodPost
		}
	case "fork":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || !validRepoName(fs.Arg(1)) {
			return errors.New("usage: trace api fork -url URL -user USER -token-file FILE SOURCE/OWNER TARGET/OWNER")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/fork"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"name": fs.Arg(1)})
		body = string(payload)
	case "archive", "restore":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE OWNER/NAME", args[0])
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/" + args[0]
		method = http.MethodPost
	case "delete":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api delete -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0)
		method = http.MethodDelete
	case "transfer":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || !validRepoName(fs.Arg(1)) {
			return errors.New("usage: trace api transfer -url URL -user USER -token-file FILE OWNER/NAME NEW_OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/transfer"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"name": fs.Arg(1)})
		body = string(payload)
	case "visibility":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api visibility -url URL -user USER -token-file FILE -public=true|false OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/visibility"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]bool{"public": *public})
		body = string(payload)
	case "secret-list":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api secret-list -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/secrets"
	case "secret-set":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *secretName == "" || (*secretValue == "" && *secretFile == "") {
			return errors.New("usage: trace api secret-set -url URL -user USER -token-file FILE -secret NAME (-secret-value VALUE|-secret-file FILE) OWNER/NAME")
		}
		value := *secretValue
		if *secretFile != "" {
			b, readErr := os.ReadFile(*secretFile)
			if readErr != nil {
				return fmt.Errorf("read secret file: %w", readErr)
			}
			value = strings.TrimSuffix(string(b), "\n")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/secrets/" + url.PathEscape(*secretName)
		method = http.MethodPut
		payload, _ := json.Marshal(map[string]string{"value": value})
		body = string(payload)
	case "secret-delete":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *secretName == "" {
			return errors.New("usage: trace api secret-delete -url URL -user USER -token-file FILE -secret NAME OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/secrets/" + url.PathEscape(*secretName)
		method = http.MethodDelete
	case "pages":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api pages -url URL -user USER -token-file FILE [-pages-branch BRANCH] [-pages-root DIR] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pages"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]any{"enabled": true, "branch": *pagesBranch, "root": *pagesRoot})
		body = string(payload)
	case "pages-disable":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api pages-disable -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pages"
		method = http.MethodPost
		body = `{"enabled":false}`
	case "raw":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *rawPath == "" {
			return errors.New("usage: trace api raw -url URL -user USER -token-file FILE -path FILE [-ref BRANCH] [-output FILE] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/raw?ref=" + url.QueryEscape(*ref) + "&path=" + url.QueryEscape(*rawPath)
	case "search-index":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api search-index -url URL -user USER -token-file FILE [-ref BRANCH] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/search-index?ref=" + url.QueryEscape(*ref)
		method = http.MethodPost
	case "audit":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api audit -url URL -user USER -token-file FILE [-limit N]")
		}
		path = "/api/v1/audit?limit=" + url.QueryEscape(fmt.Sprint(*limit))
	case "notifications":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api notifications -url URL -user USER -token-file FILE [-state all]")
		}
		path = "/api/v1/notifications?unread=" + fmt.Sprint(*state != "all")
	case "notification-read":
		if fs.NArg() != 1 {
			return errors.New("usage: trace api notification-read -url URL -user USER -token-file FILE ID")
		}
		path = "/api/v1/notifications/" + fs.Arg(0) + "/read"
		method = http.MethodPost
	case "branches", "commits":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE OWNER/NAME", args[0])
		}
		path = "/api/v1/repos/" + fs.Arg(0)
		if args[0] == "branches" {
			path += "/branches"
		} else {
			path += "/commits?limit=" + url.QueryEscape(fmt.Sprint(*limit))
		}
	case "branch-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *ref == "" || *branchName == "" {
			return errors.New("usage: trace api branch-create -url URL -user USER -token-file FILE -ref SOURCE -branch NAME OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/branches"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"name": *branchName, "from": *ref})
		body = string(payload)
	case "branch-delete":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api branch-delete -url URL -user USER -token-file FILE OWNER/NAME BRANCH")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/branches/" + url.PathEscape(fs.Arg(1))
		method = http.MethodDelete
	case "statuses":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api statuses -url URL -user USER -token-file FILE OWNER/NAME COMMIT")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/statuses/" + url.PathEscape(fs.Arg(1))
	case "status-set":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || *statusContext == "" || *statusState == "" {
			return errors.New("usage: trace api status-set -url URL -user USER -token-file FILE -context NAME -status STATE OWNER/NAME COMMIT")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/statuses/" + url.PathEscape(fs.Arg(1))
		method = http.MethodPost
		payload, _ := json.Marshal(commitStatus{Context: *statusContext, State: *statusState, TargetURL: *statusURL, Description: *description})
		body = string(payload)
	case "topics":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api topics -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/topics"
	case "topics-set":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api topics-set -url URL -user USER -token-file FILE -topics TOPIC,... OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/topics"
		method = http.MethodPatch
		values := []string{}
		if strings.TrimSpace(*topics) != "" {
			for _, value := range strings.Split(*topics, ",") {
				if value = strings.TrimSpace(value); value != "" {
					values = append(values, value)
				}
			}
		}
		payload, _ := json.Marshal(map[string]any{"topics": values})
		body = string(payload)
	case "projects":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api projects -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/projects"
	case "project-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*projectName) == "" {
			return errors.New("usage: trace api project-create -project NAME [-description TEXT] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/projects"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"name": *projectName, "description": *description})
		body = string(payload)
	case "project-card-add":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || *cardNumber < 1 || (*cardKind != "issue" && *cardKind != "pull") {
			return errors.New("usage: trace api project-card-add -card-kind issue|pull -card-number N OWNER/NAME PROJECT_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/projects/" + fs.Arg(1) + "/cards"
		method = http.MethodPost
		payload, _ := json.Marshal(projectCard{Kind: *cardKind, Number: *cardNumber, Title: *title, Column: *cardColumn})
		body = string(payload)
	case "project-card-move":
		if fs.NArg() != 3 || !validRepoName(fs.Arg(0)) || *cardColumn == "" {
			return errors.New("usage: trace api project-card-move -column NAME OWNER/NAME PROJECT_ID CARD_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/projects/" + fs.Arg(1) + "/cards/" + fs.Arg(2)
		method = http.MethodPatch
		payload, _ := json.Marshal(map[string]string{"column": *cardColumn})
		body = string(payload)
	case "search":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*query) == "" {
			return errors.New("usage: trace api search -url URL -user USER -token-file FILE -query TEXT [-ref BRANCH] [-scope all|code|commits|sessions] OWNER/NAME")
		}
		if *scope != "all" && *scope != "code" && *scope != "commits" && *scope != "sessions" {
			return errors.New("-scope must be all, code, commits, or sessions")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/search?q=" + url.QueryEscape(*query) + "&ref=" + url.QueryEscape(*ref) + "&scope=" + url.QueryEscape(*scope)
	case "search-all":
		if fs.NArg() != 0 || strings.TrimSpace(*query) == "" {
			return errors.New("usage: trace api search-all -url URL -user USER -token-file FILE -query TEXT [-ref BRANCH] [-scope all|code|commits|sessions] [-after OWNER/NAME]")
		}
		if *scope != "all" && *scope != "code" && *scope != "commits" && *scope != "sessions" {
			return errors.New("-scope must be all, code, commits, or sessions")
		}
		if *after != "" && !validRepoName(*after) {
			return errors.New("-after must be an OWNER/NAME repository cursor")
		}
		path = "/api/v1/search?q=" + url.QueryEscape(*query) + "&ref=" + url.QueryEscape(*ref) + "&scope=" + url.QueryEscape(*scope) + "&after=" + url.QueryEscape(*after)
	case "manifest":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api manifest -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/federation/repos/" + fs.Arg(0) + "/manifest"
	case "federation-peers":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api federation-peers -url URL -user USER -token-file FILE")
		}
		path = "/api/v1/federation/peers"
	case "federation-conflicts":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api federation-conflicts -url URL -user USER -token-file FILE")
		}
		path = "/api/v1/federation/conflicts"
	case "federation-resolve":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *peerSource == "" || *conflictRef == "" || *conflictLocal == "" || (*conflictDelete && *conflictRemote != "") || (!*conflictDelete && *conflictRemote == "") {
			return errors.New("usage: trace api federation-resolve -url URL -user USER -token-file FILE -peer-source URL -conflict-ref FULL_REF -conflict-local OID (-conflict-remote OID|-conflict-delete) OWNER/NAME")
		}
		path = "/api/v1/federation/conflicts/resolve"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"repo": fs.Arg(0), "source": *peerSource, "ref": *conflictRef, "local_sha": *conflictLocal, "remote_sha": *conflictRemote})
		body = string(payload)
	case "federation-trust":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api federation-trust -url URL -user USER -token-file FILE")
		}
		path = "/api/v1/federation/trust"
	case "federation-trust-forget":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *peerSource == "" || !validFederationNodeID(*trustNodeID) {
			return errors.New("usage: trace api federation-trust-forget -url URL -user USER -token-file FILE -peer-source URL -node-id PINNED_ID OWNER/NAME")
		}
		path = "/api/v1/federation/trust"
		method = http.MethodDelete
		payload, _ := json.Marshal(map[string]string{"repo": fs.Arg(0), "source": *peerSource, "expected_node_id": *trustNodeID})
		body = string(payload)
	case "federation-peer-add":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *peerSource == "" || *peerTokenFile == "" {
			return errors.New("usage: trace api federation-peer-add -url URL -user USER -token-file FILE -peer-source URL -peer-user USER -peer-token-file FILE OWNER/NAME")
		}
		path = "/api/v1/federation/peers"
		method = http.MethodPost
		payload, _ := json.Marshal(federationPeer{Repo: fs.Arg(0), Source: *peerSource, Username: *peerUser, TokenFile: *peerTokenFile})
		body = string(payload)
	case "federation-peer-remove":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api federation-peer-remove -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/federation/peers/" + fs.Arg(0)
		method = http.MethodDelete
	case "federation-sync":
		if fs.NArg() != 0 {
			return errors.New("usage: trace api federation-sync -url URL -user USER -token-file FILE")
		}
		path = "/api/v1/federation/peers/sync"
		method = http.MethodPost
	case "policy":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api policy -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/policy"
	case "policy-set":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *requiredApprovals < 0 {
			return errors.New("usage: trace api policy-set -url URL -user USER -token-file FILE -required-approvals N [-required-checks job,...] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/policy"
		method = http.MethodPatch
		checks := []string{}
		if strings.TrimSpace(*requiredChecks) != "" {
			for _, check := range strings.Split(*requiredChecks, ",") {
				checks = append(checks, strings.TrimSpace(check))
			}
		}
		branches := []string{}
		for _, branch := range strings.Split(*protectedBranches, ",") {
			if branch = strings.TrimSpace(branch); branch != "" {
				branches = append(branches, branch)
			}
		}
		payload, _ := json.Marshal(repoPolicy{RequiredApprovals: *requiredApprovals, RequiredChecks: checks, ProtectedBranches: branches, RequireCodeOwners: *requireCodeOwners})
		body = string(payload)
	case "agent-sessions":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api agent-sessions -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions"
	case "agent-bundle-export":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *rawOutput == "" {
			return errors.New("usage: trace api agent-bundle-export -url URL -user USER -token-file FILE -output NEW_FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions/bundle"
	case "agent-bundle-import":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *bundleFile == "" || !validFederationNodeID(*bundleExpectedNodeID) {
			return errors.New("usage: trace api agent-bundle-import -url URL -user USER -token-file FILE -bundle-file FILE -expected-node-id ID OWNER/NAME")
		}
		encoded, err := os.ReadFile(*bundleFile)
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
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions/bundle"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]any{"expected_node_id": *bundleExpectedNodeID, "bundle": bundle})
		body = string(payload)
	case "agent-session-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*agentName) == "" {
			return errors.New("usage: trace api agent-session-create -url URL -user USER -token-file FILE -agent NAME [-ref BRANCH] [-description TEXT] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"agent": *agentName, "ref": *ref, "summary": *description})
		body = string(payload)
	case "agent-session":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api agent-session -url URL -user USER -token-file FILE OWNER/NAME SESSION_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions/" + fs.Arg(1)
	case "agent-session-checkpoint":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api agent-session-checkpoint -url URL -user USER -token-file FILE [-ref BRANCH] -description TEXT OWNER/NAME SESSION_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/agent-sessions/" + fs.Arg(1) + "/checkpoints"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"ref": *ref, "summary": *description, "state": *checkpointState})
		body = string(payload)
	case "pulls":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api pulls -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pulls?state=" + url.QueryEscape(*state)
	case "pull-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*title) == "" || *source == "" {
			return errors.New("usage: trace api pull-create -url URL -user USER -token-file FILE -title TITLE -source BRANCH [-target BRANCH] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pulls"
		method = http.MethodPost
		var pullLabelValues, assignees, reviewers []string
		for _, value := range strings.Split(*labels, ",") {
			if value = strings.TrimSpace(value); value != "" {
				pullLabelValues = append(pullLabelValues, value)
			}
		}
		for _, value := range strings.Split(*pullAssignees, ",") {
			if value = strings.TrimSpace(value); value != "" {
				assignees = append(assignees, value)
			}
		}
		for _, value := range strings.Split(*pullReviewers, ",") {
			if value = strings.TrimSpace(value); value != "" {
				reviewers = append(reviewers, value)
			}
		}
		payload, _ := json.Marshal(map[string]any{"title": *title, "description": *description, "source": *source, "target": *target, "draft": *draft, "labels": pullLabelValues, "assignees": assignees, "reviewers": reviewers})
		body = string(payload)
	case "pull-comment":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || *bodyText == "" {
			return errors.New("usage: trace api pull-comment -url URL -user USER -token-file FILE -body TEXT [-file PATH -line N] OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pulls/" + fs.Arg(1) + "/comments"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]any{"body": *bodyText, "file": *commentFile, "line": *commentLine, "side": *commentSide})
		body = string(payload)
	case "pull-approve", "pull-merge", "pull-close", "pull-ready":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE OWNER/NAME ID", args[0])
		}
	case "merge-queue", "merge-queue-enqueue", "merge-queue-process":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE OWNER/NAME", args[0])
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/merge-queue"
		if args[0] == "merge-queue-enqueue" {
			if *mergeQueuePR < 1 {
				return errors.New("-pull-request is required for merge-queue-enqueue")
			}
			path += "/enqueue"
			method = http.MethodPost
			payload, _ := json.Marshal(map[string]any{"pull_request_id": *mergeQueuePR, "strategy": *mergeStrategy})
			body = string(payload)
		} else if args[0] == "merge-queue-process" {
			path += "/process"
			method = http.MethodPost
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/pulls/" + fs.Arg(1) + "/" + strings.TrimPrefix(args[0], "pull-")
		method = http.MethodPost
		if args[0] == "pull-merge" {
			payload, _ := json.Marshal(map[string]string{"strategy": *mergeStrategy})
			body = string(payload)
		}
	case "issues":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api issues -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/issues?state=" + url.QueryEscape(*state)
	case "issue-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*title) == "" {
			return errors.New("usage: trace api issue-create -url URL -user USER -token-file FILE -title TITLE [-body TEXT] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/issues"
		method = http.MethodPost
		var issueLabels []string
		if *labels != "" {
			for _, label := range strings.Split(*labels, ",") {
				issueLabels = append(issueLabels, strings.TrimSpace(label))
			}
		}
		payload, _ := json.Marshal(map[string]any{"title": *title, "body": *description, "assignee": *assignee, "labels": issueLabels})
		body = string(payload)
	case "issue-comment":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || *bodyText == "" {
			return errors.New("usage: trace api issue-comment -url URL -user USER -token-file FILE -body TEXT OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/issues/" + fs.Arg(1) + "/comments"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"body": *bodyText})
		body = string(payload)
	case "issue-close", "issue-reopen":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return fmt.Errorf("usage: trace api %s -url URL -user USER -token-file FILE OWNER/NAME ID", args[0])
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/issues/" + fs.Arg(1)
		method = http.MethodPatch
		issueState := "closed"
		if args[0] == "issue-reopen" {
			issueState = "open"
		}
		payload, _ := json.Marshal(map[string]string{"state": issueState})
		body = string(payload)
	case "milestones":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api milestones -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/milestones?state=" + url.QueryEscape(*state)
	case "milestone-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*title) == "" {
			return errors.New("usage: trace api milestone-create -url URL -user USER -token-file FILE -title TITLE [-due-date YYYY-MM-DD] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/milestones"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"title": *title, "description": *description, "due_date": *dueDate})
		body = string(payload)
	case "milestone-update":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api milestone-update -url URL -user USER -token-file FILE [-title TITLE] [-state open|closed] OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/milestones/" + fs.Arg(1)
		method = http.MethodPatch
		payload, _ := json.Marshal(map[string]string{"title": *title, "description": *description, "state": *state, "due_date": *dueDate})
		body = string(payload)
	case "milestone-delete":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api milestone-delete -url URL -user USER -token-file FILE OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/milestones/" + fs.Arg(1)
		method = http.MethodDelete
	case "webhooks":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api webhooks -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/webhooks"
	case "webhook-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *webhookURL == "" || *webhookSecret == "" || *webhookEvents == "" {
			return errors.New("usage: trace api webhook-create -url URL -user USER -token-file FILE -webhook-url URL -webhook-secret SECRET -events EVENT,... OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/webhooks"
		method = http.MethodPost
		var events []string
		for _, event := range strings.Split(*webhookEvents, ",") {
			events = append(events, strings.TrimSpace(event))
		}
		payload, _ := json.Marshal(map[string]any{"url": *webhookURL, "secret": *webhookSecret, "events": events})
		body = string(payload)
	case "webhook-delete":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api webhook-delete -url URL -user USER -token-file FILE OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/webhooks/" + fs.Arg(1)
		method = http.MethodDelete
	case "webhook-deliveries":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api webhook-deliveries -url URL -user USER -token-file FILE OWNER/NAME WEBHOOK_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/webhooks/" + fs.Arg(1) + "/deliveries"
	case "releases":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api releases -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/releases"
	case "release-create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || *tag == "" || strings.TrimSpace(*title) == "" {
			return errors.New("usage: trace api release-create -url URL -user USER -token-file FILE -tag TAG -title TITLE [-description TEXT] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/releases"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]any{"tag": *tag, "name": *title, "body": *description, "draft": *draft, "prerelease": *prerelease})
		body = string(payload)
	case "release-delete":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api release-delete -url URL -user USER -token-file FILE OWNER/NAME ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/releases/" + fs.Arg(1)
		method = http.MethodDelete
	case "release-assets":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api release-assets -url URL -user USER -token-file FILE OWNER/NAME RELEASE_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/releases/" + fs.Arg(1) + "/assets"
	case "release-asset-upload":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || !validReleaseAssetName(*assetName) || *assetFile == "" {
			return errors.New("usage: trace api release-asset-upload -url URL -user USER -token-file FILE -asset-name NAME -asset-file FILE OWNER/NAME RELEASE_ID")
		}
		assetBytes, readErr := os.ReadFile(*assetFile)
		if readErr != nil {
			return fmt.Errorf("read asset file: %w", readErr)
		}
		if len(assetBytes) > maxReleaseAssetSize {
			return errors.New("release asset exceeds 100 MiB")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/releases/" + fs.Arg(1) + "/assets?name=" + url.QueryEscape(*assetName)
		method = http.MethodPost
		body = string(assetBytes)
		contentType = "application/octet-stream"
	case "stars":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api stars -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/stars"
	case "star":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api star -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/stars"
		method = http.MethodPut
	case "unstar":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api unstar -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/stars"
		method = http.MethodDelete
	case "watchers":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api watchers -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/watchers"
	case "watch":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api watch -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/watchers"
		method = http.MethodPut
	case "unwatch":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api unwatch -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/watchers"
		method = http.MethodDelete
	case "actions":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api actions -url URL -user USER -token-file FILE OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/actions/runs"
	case "action-run":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api action-run -url URL -user USER -token-file FILE [-ref BRANCH] OWNER/NAME")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/actions/runs"
		method = http.MethodPost
		payload, _ := json.Marshal(map[string]string{"ref": *ref})
		body = string(payload)
	case "action-view":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api action-view -url URL -user USER -token-file FILE OWNER/NAME RUN_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/actions/runs/" + fs.Arg(1)
	case "action-cancel":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace api action-cancel -url URL -user USER -token-file FILE OWNER/NAME RUN_ID")
		}
		path = "/api/v1/repos/" + fs.Arg(0) + "/actions/runs/" + fs.Arg(1) + "/cancel"
		method = http.MethodPost
	default:
		return errors.New("usage: trace api <repos|audit|branches|commits|pulls|issues|milestones|webhooks|...> ...")
	}
	requestURL := *endpoint
	requestURL.Path = strings.TrimRight(endpoint.Path, "/") + path
	requestURL.RawQuery = ""
	if strings.Contains(path, "?") {
		parts := strings.SplitN(path, "?", 2)
		requestURL.Path = strings.TrimRight(endpoint.Path, "/") + parts[0]
		requestURL.RawQuery = parts[1]
	}
	req, err := http.NewRequest(method, requestURL.String(), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(*user, token)
	if body != "" {
		req.Header.Set("Content-Type", contentType)
		if contentType == "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if args[0] == "release-asset-upload" {
		req.Header.Set("X-Asset-Name", *assetName)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request Trace API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("Trace API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode == http.StatusNoContent {
		fmt.Println("ok")
		return nil
	}
	if args[0] == "raw" {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRawFile+1))
		if readErr != nil {
			return readErr
		}
		if len(data) > maxRawFile {
			return errors.New("raw response exceeds 8 MiB limit")
		}
		if *rawOutput != "" {
			if err := os.WriteFile(*rawOutput, data, 0600); err != nil {
				return err
			}
			fmt.Println(*rawOutput)
		} else {
			_, _ = os.Stdout.Write(data)
		}
		return nil
	}
	if args[0] == "agent-bundle-export" {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAgentBundleBytes+64<<10+1))
		if readErr != nil {
			return readErr
		}
		if len(data) > maxAgentBundleBytes+64<<10 {
			return errors.New("agent bundle response exceeds size limit")
		}
		file, err := os.OpenFile(*rawOutput, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			_ = os.Remove(*rawOutput)
			return err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(*rawOutput)
			return err
		}
		fmt.Println(*rawOutput)
		return nil
	}
	var value any
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return fmt.Errorf("decode Trace API response: %w", err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
