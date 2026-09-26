package main

// This file implements Trace's deliberately small local CI runner. Workflows
// are JSON because the core binary has no YAML dependency. A workflow is read
// from the commit being tested at .trace/workflow.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	actionsConfigPath = ".trace/workflow.json"
	maxActionLog      = 1 << 20
	maxActionDuration = 15 * time.Minute
	maxActionWorkers  = 4
)

type workflowConfig struct {
	Name           string        `json:"name,omitempty"`
	Schedule       string        `json:"schedule,omitempty"`
	Sandbox        bool          `json:"sandbox,omitempty"`
	SandboxRuntime string        `json:"sandbox_runtime,omitempty"`
	SandboxImage   string        `json:"sandbox_image,omitempty"`
	Jobs           []workflowJob `json:"jobs"`
}

type workflowJob struct {
	Name      string   `json:"name"`
	Run       []string `json:"run"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type actionArtifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"-"`
}

type actionJob struct {
	ID         int              `json:"id"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	ExitCode   int              `json:"exit_code"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Log        string           `json:"log,omitempty"`
	Artifacts  []actionArtifact `json:"artifacts,omitempty"`
}

type actionRun struct {
	ID          int         `json:"id"`
	Repo        string      `json:"repo"`
	Ref         string      `json:"ref"`
	Commit      string      `json:"commit"`
	Status      string      `json:"status"`
	TriggeredBy string      `json:"triggered_by"`
	CreatedAt   time.Time   `json:"created_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	Jobs        []actionJob `json:"jobs"`
}

type actionDB struct {
	NextRunID       int                  `json:"next_run_id"`
	Runs            []actionRun          `json:"runs"`
	LastScheduledAt map[string]time.Time `json:"last_scheduled_at,omitempty"`
}

// CI runner policies selected by the operator with `trace serve -actions`.
// Workflow files are repository content that any writer can change, so they
// may opt into a sandbox but can never opt out of the operator's policy.
const (
	actionsModeOff       = "off"
	actionsModeSandboxed = "sandboxed"
	actionsModeTrusted   = "trusted"
)

func parseActionsMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case actionsModeOff:
		return actionsModeOff, nil
	case actionsModeSandboxed, "":
		return actionsModeSandboxed, nil
	case actionsModeTrusted:
		return actionsModeTrusted, nil
	default:
		return "", errors.New("-actions must be off, sandboxed, or trusted")
	}
}

// actionsPolicy returns the effective runner policy; an unset store defaults
// to sandboxed so no code path runs unsandboxed jobs without operator consent.
func (s *store) actionsPolicy() string {
	mode, err := parseActionsMode(s.actionsMode)
	if err != nil {
		return actionsModeSandboxed
	}
	return mode
}

// checkActionsPolicy refuses a workflow that the operator's policy forbids.
func (s *store) checkActionsPolicy(config workflowConfig) error {
	switch s.actionsPolicy() {
	case actionsModeOff:
		return errors.New("CI actions are disabled on this node (trace serve -actions off)")
	case actionsModeSandboxed:
		if !config.Sandbox {
			return errors.New("this node runs only sandboxed workflows: set \"sandbox\":true in .trace/workflow.json, or an operator can start trace serve -actions trusted for trusted repositories")
		}
	}
	return nil
}

var actionRunMu sync.Mutex
var actionCancelMu sync.Mutex
var actionCancels = map[int]context.CancelFunc{}
var actionSlotMu sync.Mutex
var actionSlots = map[string]chan struct{}{}

func actionWorkerSlot(root string) chan struct{} {
	actionSlotMu.Lock()
	defer actionSlotMu.Unlock()
	if slot, ok := actionSlots[root]; ok {
		return slot
	}
	slot := make(chan struct{}, maxActionWorkers)
	actionSlots[root] = slot
	return slot
}

func (a *app) apiActions(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			runs, err := a.store.listActionRuns(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list action runs")
				return
			}
			writeJSON(w, http.StatusOK, runs)
			return
		}
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.Admin && roleFor(u, repo) != "write" {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Ref string `json:"ref"`
		}
		if r.Body != nil {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil && err != io.EOF {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
		run, err := a.store.actionRun(repo, input.Ref, username)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "action.run", repo, fmt.Sprint(run.ID), map[string]any{"ref": run.Ref, "commit": run.Commit})
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "action run not found")
		return
	}
	if len(tail) == 1 && r.Method == http.MethodGet {
		run, err := actionRunByID(a.store, id)
		if err != nil || run.Repo != repo {
			apiError(w, http.StatusNotFound, "action run not found")
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}
	if len(tail) == 2 && tail[1] == "cancel" && r.Method == http.MethodPost {
		run, err := actionRunByID(a.store, id)
		if err != nil || run.Repo != repo {
			apiError(w, http.StatusNotFound, "action run not found")
			return
		}
		if run.Status == "success" || run.Status == "failure" || run.Status == "cancelled" {
			apiError(w, http.StatusConflict, "action run is already finished")
			return
		}
		if !cancelActionRun(id) {
			apiError(w, http.StatusConflict, "action run is no longer running")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "cancelling"})
		return
	}
	if len(tail) >= 3 && tail[1] == "artifacts" && r.Method == http.MethodGet {
		artifact, err := actionArtifactByName(a.store, id, strings.Join(tail[2:], "/"))
		if err != nil {
			apiError(w, http.StatusNotFound, "artifact not found")
			return
		}
		run, err := actionRunByID(a.store, id)
		if err != nil || run.Repo != repo {
			apiError(w, http.StatusNotFound, "action run not found")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(artifact.Name)+`"`)
		http.ServeFile(w, r, artifact.Path)
		return
	}
	apiError(w, http.StatusNotFound, "not found")
}

func (s *store) loadActions() (actionDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "actions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return actionDB{NextRunID: 1, LastScheduledAt: map[string]time.Time{}}, nil
	}
	if err != nil {
		return actionDB{}, err
	}
	var db actionDB
	if err := json.Unmarshal(b, &db); err != nil {
		return actionDB{}, fmt.Errorf("read actions: %w", err)
	}
	if db.NextRunID < 1 {
		db.NextRunID = 1
	}
	if db.LastScheduledAt == nil {
		db.LastScheduledAt = map[string]time.Time{}
	}
	return db, nil
}

func (s *store) saveActions(db actionDB) error {
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".actions-*")
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
	return os.Rename(name, filepath.Join(s.root, "actions.json"))
}

func (s *store) listActionRuns(repo string) ([]actionRun, error) {
	actionRunMu.Lock()
	defer actionRunMu.Unlock()
	db, err := s.loadActions()
	if err != nil {
		return nil, err
	}
	out := make([]actionRun, 0)
	for _, run := range db.Runs {
		if repo == "" || run.Repo == repo {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *store) actionRun(repo, ref, actor string) (actionRun, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return actionRun{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return actionRun{}, errors.New("repository not found")
	}
	if s.actionsPolicy() == actionsModeOff {
		return actionRun{}, s.checkActionsPolicy(workflowConfig{})
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "main"
	}
	commit, err := gitActionOutput(path, "rev-parse", "refs/heads/"+ref)
	if err != nil {
		return actionRun{}, fmt.Errorf("resolve ref: %w", err)
	}
	config, err := readWorkflow(path, commit)
	if err != nil {
		return actionRun{}, err
	}
	if len(config.Jobs) == 0 {
		return actionRun{}, errors.New("workflow has no jobs")
	}
	if err := s.checkActionsPolicy(config); err != nil {
		return actionRun{}, err
	}
	secrets, err := s.actionSecrets(repo)
	if err != nil {
		return actionRun{}, fmt.Errorf("load action secrets: %w", err)
	}

	actionRunMu.Lock()
	db, err := s.loadActions()
	if err != nil {
		actionRunMu.Unlock()
		return actionRun{}, err
	}
	run := actionRun{ID: db.NextRunID, Repo: repo, Ref: ref, Commit: commit, Status: "queued", TriggeredBy: actor, CreatedAt: time.Now().UTC()}
	db.NextRunID++
	db.Runs = append(db.Runs, run)
	if err := s.saveActions(db); err != nil {
		actionRunMu.Unlock()
		return actionRun{}, err
	}
	actionRunMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	actionCancelMu.Lock()
	actionCancels[run.ID] = cancel
	actionCancelMu.Unlock()
	go s.executeActionRun(ctx, run, config, path, secrets)
	return run, nil
}

// scheduleActionRuns evaluates opt-in workflow schedules for the repository's
// main branch. It records the last evaluation before starting a run so a
// slow or repeatedly ticking scheduler cannot enqueue duplicates.
func (s *store) scheduleActionRuns() error {
	if s.actionsPolicy() == actionsModeOff {
		return nil
	}
	now := time.Now().UTC()
	type candidate struct {
		repo string
		ref  string
	}
	var candidates []candidate
	owners, err := os.ReadDir(s.repos)
	if err != nil {
		return err
	}
	actionRunMu.Lock()
	db, err := s.loadActions()
	if err != nil {
		actionRunMu.Unlock()
		return err
	}
	for _, owner := range owners {
		if !owner.IsDir() || !namePattern.MatchString(owner.Name()) {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(s.repos, owner.Name()))
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
				continue
			}
			base := strings.TrimSuffix(entry.Name(), ".git")
			if !namePattern.MatchString(base) {
				continue
			}
			repo := owner.Name() + "/" + base
			path := filepath.Join(s.repos, owner.Name(), entry.Name())
			if isArchived(path) || isMirror(path) {
				continue
			}
			commit, revErr := gitActionOutput(path, "rev-parse", "refs/heads/main")
			if revErr != nil {
				continue
			}
			config, readErr := readWorkflow(path, commit)
			if readErr != nil || strings.TrimSpace(config.Schedule) == "" || s.checkActionsPolicy(config) != nil {
				continue
			}
			d, parseErr := time.ParseDuration(config.Schedule)
			if parseErr != nil {
				continue
			}
			key := repo + "\x00main"
			last := db.LastScheduledAt[key]
			if !last.IsZero() && now.Sub(last) < d {
				continue
			}
			// Do not queue another scheduled run while a previous scheduled
			// run for this repository is in flight. Push and manual runs are
			// independent triggers and may already be occupying worker slots;
			// the bounded queue provides backpressure for those runs.
			busy := false
			for _, run := range db.Runs {
				if run.Repo == repo && run.TriggeredBy == "scheduler" && (run.Status == "queued" || run.Status == "running") {
					busy = true
					break
				}
			}
			if busy {
				continue
			}
			db.LastScheduledAt[key] = now
			candidates = append(candidates, candidate{repo: repo, ref: "main"})
		}
	}
	if len(candidates) > 0 {
		if err := s.saveActions(db); err != nil {
			actionRunMu.Unlock()
			return err
		}
	}
	actionRunMu.Unlock()
	for _, item := range candidates {
		if _, err := s.actionRun(item.repo, item.ref, "scheduler"); err != nil {
			log.Printf("trace scheduled action failed for %s: %v", item.repo, err)
		}
	}
	return nil
}

func cancelActionRun(id int) bool {
	actionCancelMu.Lock()
	cancel, ok := actionCancels[id]
	actionCancelMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func readWorkflow(repoPath, commit string) (workflowConfig, error) {
	cmd := exec.Command("git", "--git-dir", repoPath, "show", commit+":"+actionsConfigPath)
	b, err := cmd.Output()
	if err != nil {
		return workflowConfig{}, errors.New("workflow file .trace/workflow.json is missing at this commit")
	}
	var config workflowConfig
	if err := json.Unmarshal(b, &config); err != nil {
		return workflowConfig{}, fmt.Errorf("invalid workflow JSON: %w", err)
	}
	config.SandboxRuntime = strings.ToLower(strings.TrimSpace(config.SandboxRuntime))
	if config.SandboxRuntime != "" && !config.Sandbox {
		return workflowConfig{}, errors.New("sandbox_runtime requires sandbox:true")
	}
	if config.SandboxRuntime != "" && config.SandboxRuntime != "macos" && config.SandboxRuntime != "docker" {
		return workflowConfig{}, errors.New("sandbox_runtime must be macos or docker")
	}
	if config.Sandbox && config.SandboxRuntime == "docker" && strings.TrimSpace(config.SandboxImage) == "" {
		return workflowConfig{}, errors.New("sandbox_image is required for docker sandboxing")
	}
	if len(config.Jobs) > 32 {
		return workflowConfig{}, errors.New("workflow has too many jobs")
	}
	if strings.TrimSpace(config.Schedule) != "" {
		d, err := time.ParseDuration(config.Schedule)
		if err != nil || d < time.Minute || d > 24*time.Hour {
			return workflowConfig{}, errors.New("workflow schedule must be between 1m and 24h")
		}
	}
	for i := range config.Jobs {
		if strings.TrimSpace(config.Jobs[i].Name) == "" || len(config.Jobs[i].Run) == 0 {
			return workflowConfig{}, errors.New("each workflow job needs a name and at least one run command")
		}
		if len(config.Jobs[i].Run) > 64 {
			return workflowConfig{}, errors.New("workflow job has too many commands")
		}
	}
	return config, nil
}

func gitActionOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--git-dir", repo}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *store) executeActionRun(ctx context.Context, run actionRun, config workflowConfig, repoPath string, secrets map[string]string) {
	slot := actionWorkerSlot(s.root)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		s.finishActionRun(run.ID, "cancelled", []actionJob{{ID: 1, Name: "queue", Status: "cancelled", ExitCode: 1, Log: "cancelled while queued"}})
		return
	}
	defer func() {
		actionCancelMu.Lock()
		delete(actionCancels, run.ID)
		actionCancelMu.Unlock()
	}()
	// Each run gets a private directory holding the checkout (src) and a
	// scratch TMPDIR (tmp); both are writable by the job and removed after.
	runDir := filepath.Join(s.root, "action-work", fmt.Sprint(run.ID))
	workspace := filepath.Join(runDir, "src")
	scratch := filepath.Join(runDir, "tmp")
	defer os.RemoveAll(runDir)
	if err := os.MkdirAll(scratch, 0700); err != nil {
		s.finishActionRun(run.ID, "failure", []actionJob{{ID: 1, Name: "checkout", Status: "failure", ExitCode: 1, Log: "cannot create the run directory"}})
		return
	}
	// Give jobs canonical paths: a sandbox cannot traverse symlinks such as
	// macOS's /var -> /private/var that it is not allowed to read.
	if resolved, err := filepath.EvalSymlinks(runDir); err == nil {
		workspace, scratch = filepath.Join(resolved, "src"), filepath.Join(resolved, "tmp")
	}
	if config.Sandbox {
		runtimeName := config.SandboxRuntime
		if runtimeName == "" {
			runtimeName = "macos"
		}
		if runtimeName == "macos" && (runtime.GOOS != "darwin" || !commandAvailable("sandbox-exec")) {
			s.finishActionRun(run.ID, "failure", []actionJob{{ID: 1, Name: "sandbox", Status: "failure", ExitCode: 1, Log: "macOS sandboxing requires sandbox-exec; refusing unsafe fallback"}})
			return
		}
		if runtimeName == "docker" && !commandAvailable("docker") {
			s.finishActionRun(run.ID, "failure", []actionJob{{ID: 1, Name: "sandbox", Status: "failure", ExitCode: 1, Log: "Docker sandboxing requires the docker client; refusing unsafe fallback"}})
			return
		}
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--no-checkout", "--local", repoPath, workspace)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			s.finishActionRun(run.ID, "cancelled", []actionJob{{ID: 1, Name: "checkout", Status: "cancelled", ExitCode: 1, Log: "cancelled"}})
			return
		}
		s.finishActionRun(run.ID, "failure", []actionJob{{ID: 1, Name: "checkout", Status: "failure", ExitCode: 1, Log: string(out)}})
		return
	}
	cmd = exec.CommandContext(ctx, "git", "-C", workspace, "checkout", "--detach", run.Commit)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			s.finishActionRun(run.ID, "cancelled", []actionJob{{ID: 1, Name: "checkout", Status: "cancelled", ExitCode: 1, Log: "cancelled"}})
			return
		}
		s.finishActionRun(run.ID, "failure", []actionJob{{ID: 1, Name: "checkout", Status: "failure", ExitCode: 1, Log: string(out)}})
		return
	}
	jobs := make([]actionJob, 0, len(config.Jobs))
	status := "success"
	for i, spec := range config.Jobs {
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		job := actionJob{ID: i + 1, Name: spec.Name, Status: "running", StartedAt: time.Now().UTC()}
		var logText strings.Builder
		for commandIndex, commandText := range spec.Run {
			if logText.Len() < maxActionLog {
				logText.WriteString("$ ")
				logText.WriteString(commandText)
				logText.WriteByte('\n')
			}
			commandCtx, cancel := context.WithTimeout(ctx, maxActionDuration)
			cmd, commandErr := buildActionCommand(commandCtx, actionCommandSpec{
				Command:   commandText,
				Workspace: workspace,
				Scratch:   scratch,
				Sandbox:   config.Sandbox,
				Runtime:   config.SandboxRuntime,
				Image:     config.SandboxImage,
				Env:       actionJobEnv(run.Repo, run.Commit, workspace, scratch, secrets),
				Name:      fmt.Sprintf("trace-run-%d-%d-%d", run.ID, job.ID, commandIndex+1),
			})
			if commandErr != nil {
				job.Status, job.ExitCode = "failure", 1
				logText.WriteString(commandErr.Error())
				status = "failure"
				cancel()
				break
			}
			// One shared writer: os/exec then serializes stdout and stderr
			// writes, and the cap keeps unbounded output out of memory.
			output := &cappedBuffer{limit: maxActionLog - logText.Len()}
			cmd.Stdout, cmd.Stderr = output, output
			err := runActionCommand(cmd)
			timedOut := commandCtx.Err() != nil
			cancel()
			logText.Write(output.Bytes())
			if timedOut && ctx.Err() == nil {
				logText.WriteString("\nTrace: step exceeded the 15-minute limit and was stopped\n")
			}
			if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil && !timedOut {
				// The step exited successfully but left background processes
				// holding its output open; they were terminated with the group.
				logText.WriteString("\nTrace: stopped background processes left running by this step\n")
				err = nil
			}
			if err != nil {
				job.Status, job.ExitCode = "failure", 1
				if ctx.Err() != nil {
					job.Status = "cancelled"
					status = "cancelled"
				}
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
					job.ExitCode = exitErr.ExitCode()
				}
				if status != "cancelled" {
					status = "failure"
				}
				break
			}
		}
		job.Log = logText.String()
		if job.Status == "running" {
			job.Status = "success"
		}
		if job.Status == "success" {
			job.Artifacts = collectActionArtifacts(s.root, run.ID, job.ID, workspace, spec.Artifacts)
		}
		job.FinishedAt = time.Now().UTC()
		jobs = append(jobs, job)
		if status == "failure" || status == "cancelled" {
			break
		}
	}
	s.finishActionRun(run.ID, status, jobs)
}

// actionJobEnv is the complete environment a job sees: the documented TRACE_*
// variables, CI, the repository's TRACE_SECRET_* values, and a minimal
// PATH/HOME/TMPDIR/locale. The Trace service environment is never inherited,
// so server credentials in it are not readable by workflows.
func actionJobEnv(repo, commit, home, tmpDir string, secrets map[string]string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	env := []string{"PATH=" + path, "HOME=" + home, "TMPDIR=" + tmpDir}
	for _, name := range []string{"LANG", "LC_ALL"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	env = append(env, "TRACE_REPOSITORY="+repo, "TRACE_COMMIT="+commit, "CI=true")
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+secrets[name])
	}
	return env
}

// actionCommandSpec describes one workflow step for buildActionCommand.
type actionCommandSpec struct {
	Command   string
	Workspace string   // checkout, the working directory
	Scratch   string   // optional extra job-writable directory (TMPDIR; /tmp in Docker)
	Sandbox   bool     // request sandbox-exec or Docker isolation
	Runtime   string   // "macos" (default) or "docker"
	Image     string   // Docker image
	Env       []string // complete job environment, KEY=VALUE
	Name      string   // unique Docker container name
}

// dockerForwardedEnv lists the job variables passed into a container. The
// container keeps the image's own PATH and HOME; values are read by the
// docker client from its environment, so secrets never appear in argv.
func dockerForwardedEnv(env []string) []string {
	var names []string
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		switch {
		case name == "TRACE_REPOSITORY", name == "TRACE_COMMIT", name == "CI", strings.HasPrefix(name, "TRACE_SECRET_"):
			names = append(names, name)
		}
	}
	return names
}

// buildActionCommand returns the runner command for one workflow step with
// its environment, working directory, and cancellation wired up. Local and
// sandbox-exec steps run in their own process group so cancellation, the
// step timeout, and step completion can stop every process they started.
// Docker steps run in a named container that cancellation kills explicitly,
// because killing the docker client alone leaves the container running.
func buildActionCommand(ctx context.Context, spec actionCommandSpec) (*exec.Cmd, error) {
	runtimeName := spec.Runtime
	if runtimeName == "" {
		runtimeName = "macos"
	}
	var cmd *exec.Cmd
	switch {
	case !spec.Sandbox:
		cmd = exec.CommandContext(ctx, "sh", "-c", spec.Command)
	case runtimeName == "docker":
		if strings.TrimSpace(spec.Image) == "" || !commandAvailable("docker") {
			return nil, errors.New("Docker sandboxing requires docker and sandbox_image; refusing unsafe fallback")
		}
		if spec.Name == "" {
			return nil, errors.New("Docker sandboxing requires a container name")
		}
		args := []string{"run", "--rm", "--name", spec.Name, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256", "-v", spec.Workspace + ":/workspace:rw"}
		if spec.Scratch != "" {
			args = append(args, "-v", spec.Scratch+":/tmp:rw")
		}
		for _, name := range dockerForwardedEnv(spec.Env) {
			args = append(args, "-e", name)
		}
		args = append(args, "-w", "/workspace", spec.Image, "/bin/sh", "-c", spec.Command)
		cmd = exec.CommandContext(ctx, "docker", args...)
		name := spec.Name
		cmd.Cancel = func() error {
			killCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = exec.CommandContext(killCtx, "docker", "kill", name).Run()
			return cmd.Process.Kill()
		}
		// The docker client talks to the daemon with the operator's
		// environment; the container only sees the variables named above.
		cmd.Env = append(os.Environ(), spec.Env...)
	case runtimeName == "macos" && runtime.GOOS == "darwin" && commandAvailable("sandbox-exec"):
		writable := []string{spec.Workspace}
		if spec.Scratch != "" {
			writable = append(writable, spec.Scratch)
		}
		cmd = exec.CommandContext(ctx, "sandbox-exec", "-p", macOSSandboxProfile(writable...), "/bin/sh", "-c", spec.Command)
	default:
		return nil, errors.New("macOS sandboxing requires sandbox-exec; refusing unsafe fallback")
	}
	if cmd.Env == nil {
		cmd.Env = spec.Env
		if cmd.Env == nil {
			cmd.Env = []string{}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return killProcessGroup(cmd) }
	}
	cmd.Dir = spec.Workspace
	cmd.WaitDelay = actionWaitDelay
	return cmd, nil
}

// actionWaitDelay bounds how long a finished or cancelled step may keep its
// output pipes open through leftover child processes.
const actionWaitDelay = 5 * time.Second

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// runActionCommand runs a step and then stops anything it left behind in its
// process group, so background processes cannot outlive the step or keep
// running after the 15-minute limit.
func runActionCommand(cmd *exec.Cmd) error {
	err := cmd.Run()
	_ = killProcessGroup(cmd)
	return err
}

func actionCommand(ctx context.Context, commandText, workspace string, sandbox bool) (*exec.Cmd, error) {
	return buildActionCommand(ctx, actionCommandSpec{Command: commandText, Workspace: workspace, Sandbox: sandbox, Name: "trace-run-adhoc"})
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// macOSSandboxProfile allows the job to read system tool directories and to
// read and write only writableDirs. sandbox-exec matches resolved paths, so
// symlinks such as /var -> /private/var are resolved first; otherwise every
// write to the workspace is denied. The root directory entry and /bin/sh's
// selector link must be readable for the shell to start on current macOS,
// and /dev/null is needed for ordinary redirections.
func macOSSandboxProfile(writableDirs ...string) string {
	quote := func(value string) string { return strconv.Quote(value) }
	profile := "(version 1)\n" +
		"(deny default)\n" +
		"(allow process-fork)\n" +
		"(allow process-exec)\n" +
		"(allow signal (target self))\n" +
		"(allow file-read* (literal \"/\") (literal \"/private/var/select/sh\"))\n" +
		"(allow file-read* file-write-data (literal \"/dev/null\"))\n" +
		"(allow file-read* (subpath \"/bin\") (subpath \"/usr\") (subpath \"/System\") (subpath \"/Library\") (subpath \"/opt/homebrew\"))\n"
	for _, dir := range writableDirs {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		profile += "(allow file-read* (subpath " + quote(dir) + "))\n" +
			"(allow file-write* (subpath " + quote(dir) + "))\n"
	}
	return profile
}

// Kept in a helper so the duration is easy to audit and change in one place.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

const maxActionArtifactSize = 50 << 20

// collectActionArtifacts copies declared artifacts out of a job workspace.
// The workspace is writable by the job, and this function runs in the
// unsandboxed Trace process, so every lookup goes through an os.Root bound to
// the workspace: symlinks (final or intermediate) cannot reach files outside
// it, symlinked artifacts are refused outright, and only regular files are
// copied. Files are opened non-blocking so a FIFO cannot stall the runner.
func collectActionArtifacts(root string, runID, jobID int, workspace string, patterns []string) []actionArtifact {
	ws, err := os.OpenRoot(workspace)
	if err != nil {
		return nil
	}
	defer ws.Close()
	var out []actionArtifact
	for _, pattern := range patterns {
		pattern = filepath.Clean(pattern)
		if pattern == "." || filepath.IsAbs(pattern) || pattern == ".." || strings.HasPrefix(pattern, ".."+string(filepath.Separator)) || strings.Contains(pattern, string(filepath.Separator)+".."+string(filepath.Separator)) {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(workspace, pattern))
		for _, source := range matches {
			rel, err := filepath.Rel(workspace, source)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			artifact, ok := copyActionArtifact(ws, root, runID, jobID, rel)
			if ok {
				out = append(out, artifact)
			}
		}
	}
	return out
}

func copyActionArtifact(ws *os.Root, root string, runID, jobID int, rel string) (actionArtifact, bool) {
	info, err := ws.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxActionArtifactSize {
		return actionArtifact{}, false
	}
	in, err := ws.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return actionArtifact{}, false
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > maxActionArtifactSize {
		return actionArtifact{}, false
	}
	name := filepath.ToSlash(rel)
	dest := filepath.Join(root, "artifacts", fmt.Sprint(runID), fmt.Sprint(jobID), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return actionArtifact{}, false
	}
	outFile, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return actionArtifact{}, false
	}
	written, copyErr := io.Copy(outFile, io.LimitReader(in, maxActionArtifactSize))
	closeErr := outFile.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		return actionArtifact{}, false
	}
	return actionArtifact{Name: name, Size: written, Path: dest}, true
}

func (s *store) finishActionRun(id int, status string, jobs []actionJob) {
	actionRunMu.Lock()
	defer actionRunMu.Unlock()
	db, err := s.loadActions()
	if err != nil {
		return
	}
	for i := range db.Runs {
		if db.Runs[i].ID == id {
			db.Runs[i].Status = status
			db.Runs[i].Jobs = jobs
			db.Runs[i].FinishedAt = time.Now().UTC()
			_ = s.saveActions(db)
			return
		}
	}
}

func actionRunByID(s *store, id int) (actionRun, error) {
	actionRunMu.Lock()
	defer actionRunMu.Unlock()
	db, err := s.loadActions()
	if err != nil {
		return actionRun{}, err
	}
	for _, run := range db.Runs {
		if run.ID == id {
			hydrateActionPaths(s, &run)
			return run, nil
		}
	}
	return actionRun{}, errors.New("action run not found")
}

func hydrateActionPaths(s *store, run *actionRun) {
	for ji := range run.Jobs {
		for ai := range run.Jobs[ji].Artifacts {
			run.Jobs[ji].Artifacts[ai].Path = filepath.Join(s.root, "artifacts", fmt.Sprint(run.ID), fmt.Sprint(run.Jobs[ji].ID), filepath.FromSlash(run.Jobs[ji].Artifacts[ai].Name))
		}
	}
}

func actionArtifactByName(s *store, id int, name string) (actionArtifact, error) {
	run, err := actionRunByID(s, id)
	if err != nil {
		return actionArtifact{}, err
	}
	name = filepath.ToSlash(filepath.Clean(name))
	for _, job := range run.Jobs {
		for _, artifact := range job.Artifacts {
			if artifact.Name == name {
				return artifact, nil
			}
		}
	}
	return actionArtifact{}, errors.New("artifact not found")
}
