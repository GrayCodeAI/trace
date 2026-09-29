package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestActionJobsDoNotInheritServerEnvironment checks that a workflow sees only
// the documented job variables, never the Trace service environment (cloud
// credentials, proxy tokens, and so on).
func TestActionJobsDoNotInheritServerEnvironment(t *testing.T) {
	t.Setenv("TRACE_TEST_SERVER_CREDENTIAL", "server-only-value")
	cases := []struct {
		name    string
		mode    string
		sandbox string
	}{
		{name: "local", mode: actionsModeTrusted},
		{name: "macos", mode: actionsModeSandboxed, sandbox: `"sandbox":true,"sandbox_runtime":"macos",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "macos" && (runtime.GOOS != "darwin" || !commandAvailable("sandbox-exec")) {
				t.Skip("sandbox-exec is only available on macOS")
			}
			root := filepath.Join(t.TempDir(), "node")
			if err := initData(root); err != nil {
				t.Fatal(err)
			}
			a, err := newApp(root)
			if err != nil {
				t.Fatal(err)
			}
			a.store.actionsMode = tc.mode
			if err := a.store.createRepo("team/env", false); err != nil {
				t.Fatal(err)
			}
			if err := a.store.setSecret("team/env", "DEPLOY", "repo-secret"); err != nil {
				t.Fatal(err)
			}
			pushWorkflow(t, a, root, "team/env", `{"name":"env",`+tc.sandbox+`"jobs":[{"name":"env","run":["env > env.txt","touch \"$TMPDIR/probe\""],"artifacts":["env.txt"]}]}`)
			runs, err := a.store.listActionRuns("team/env")
			if err != nil || len(runs) != 1 {
				t.Fatalf("push did not queue one run: %+v %v", runs, err)
			}
			run := waitForActionRun(t, a.store, runs[0].ID)
			if run.Status != "success" || len(run.Jobs) != 1 || len(run.Jobs[0].Artifacts) != 1 {
				t.Fatalf("env job failed: %+v", run)
			}
			b, err := os.ReadFile(run.Jobs[0].Artifacts[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			env := string(b)
			if strings.Contains(env, "TRACE_TEST_SERVER_CREDENTIAL") || strings.Contains(env, "server-only-value") {
				t.Fatalf("job inherited the server environment:\n%s", env)
			}
			for _, want := range []string{"TRACE_REPOSITORY=team/env", "TRACE_COMMIT=" + run.Commit, "CI=true", "TRACE_SECRET_DEPLOY=repo-secret", "HOME=", "TMPDIR=", "PATH="} {
				if !strings.Contains(env, want) {
					t.Fatalf("job environment is missing %q:\n%s", want, env)
				}
			}
		})
	}
}
