package managedworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestSharedTaskFolderAcrossRealExecutions(t *testing.T) {
	for name, test := range map[string]struct{ prompt, input string }{
		"environment": {"Build the plan for {{task.spec}}", `cat >/dev/null; input="$MACHINIST_OUTPUT_DIR/spec.md"`},
		"template":    {"{{task.output_dir}}/spec.md", `input=$(cat)`},
	} {
		t.Run(name, func(t *testing.T) { testTaskFileHandoff(t, test.prompt, test.input) })
	}
}

func testTaskFileHandoff(t *testing.T, buildPrompt, readInput string) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if b, e := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, b)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	definition := filepath.Join(dir, "config.toml")
	write(definition, `[commands.plan]
executor="plan"
prompt_file="plan.md"
[commands.build]
executor="build"
prompt_file="build.md"
[workflows.deliver]
steps=[{command="plan",required_outputs=["spec.md"]},{command="build",required_outputs=["result.txt"]}]
`)
	write(filepath.Join(dir, "plan.md"), "{{task.spec}}")
	write(filepath.Join(dir, "build.md"), buildPrompt)
	write(filepath.Join(repo, "plan.sh"), `#!/bin/sh
set -eu
mkdir -p "$MACHINIST_OUTPUT_DIR/__pycache__"
printf cache > "$MACHINIST_OUTPUT_DIR/__pycache__/helper.pyc"
printf cache > "$MACHINIST_OUTPUT_DIR/legacy.pyc"
printf scratch > "$MACHINIST_SCRATCH_DIR/clone.log"
cat > "$MACHINIST_OUTPUT_DIR/spec.md"
echo executed >> counter
printf '{"outcome":"complete","summary":"Planned"}' > "$MACHINIST_STEP_RESULT_PATH"
`)
	write(filepath.Join(repo, "build.sh"), `#!/bin/sh
set -eu
`+readInput+`
cat "$input" > "$MACHINIST_OUTPUT_DIR/result.txt"
printf '{"outcome":"complete","summary":"Built"}' > "$MACHINIST_STEP_RESULT_PATH"
`)
	store, err := controlplane.OpenStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := controlplane.NewServer(store, definition, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	var uploads atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && uploads.Add(1) == 1 {
			http.Error(w, "temporary storage outage", 503)
			return
		}
		server.Handler().ServeHTTP(w, r)
	}))
	defer web.Close()
	token := filepath.Join(dir, "token")
	write(token, "secret")
	worker, err := New(config.Worker{Name: "test", DataDirectory: filepath.Join(dir, "worker"), ControlPlane: config.ControlPlane{URL: web.URL, TokenFile: token}, Executors: map[string]config.Executor{"plan": {Command: []string{"./plan.sh"}}, "build": {Command: []string{"./build.sh"}}}, Repositories: map[string]config.Repository{"repo": {Path: repo}}}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := cfg.ResolveTaskWorkflow("deliver", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateTaskJob(t.Context(), protocol.Task{Title: "Artifact handoff", Spec: "requirements with literal {{stage.output_dir}}"}, "repo", "deliver", steps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		spec, e := worker.poll(t.Context())
		if e != nil || spec == nil {
			t.Fatalf("%v %v", spec, e)
		}
		completion := worker.executeWithHeartbeats(t.Context(), *spec)
		if completion.State != "succeeded" || completion.PublicationError != "" {
			t.Fatalf("%+v", completion)
		}
		if e = worker.deliver(t.Context(), spec.ID, completion); e != nil {
			t.Fatal(e)
		}
	}
	snap, err := store.Snapshot(t.Context())
	if err != nil || snap.Jobs[0].State != "succeeded" {
		b, _ := json.Marshal(snap)
		t.Fatalf("%s %v", b, err)
	}
	files, err := store.ListArtifacts(t.Context(), id)
	if err != nil || len(files) != 3 {
		t.Fatalf("%v %v", files, err)
	}
	// Destroy the worker's workspace; outputs remain available on the control plane.
	if err = os.RemoveAll(filepath.Join(dir, "worker")); err != nil {
		t.Fatal(err)
	}
	for _, a := range files {
		_, f, e := store.OpenArtifact(t.Context(), a.ID)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(f)
		f.Close()
		if e != nil || string(b) != "requirements with literal {{stage.output_dir}}" {
			t.Fatalf("%s %v", b, e)
		}
	}
	count, _ := os.ReadFile(filepath.Join(repo, "counter"))
	if strings.Count(string(count), "executed") != 1 {
		t.Fatalf("upload retry reran process: %s", count)
	}
	if uploads.Load() != 4 {
		t.Fatal(uploads.Load())
	}
}

func TestOutputCollectionRejectsSymlinksAndOversize(t *testing.T) {
	for _, kind := range []string{"directory-link", "file-link", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outputs := filepath.Join(dir, "outputs")
			outside := filepath.Join(dir, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "directory-link" {
				if err := os.Symlink(outside, outputs); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(outputs, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "file-link" {
					if err := os.Symlink(outside, filepath.Join(outputs, "link")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(outputs, "big"), []byte("12345"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			worker := &Worker{}
			if _, err := worker.publishOutputs(t.Context(), protocol.RunSpec{ArtifactLimits: &protocol.ArtifactLimits{MaxFileBytes: 4, MaxRunBytes: 4}}, outputs); err == nil {
				t.Fatal("invalid output accepted")
			}
		})
	}
}

func TestInputLocalFilesystemFailureDoesNotRetry(t *testing.T) {
	var downloads atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); _, _ = io.WriteString(w, "abc") }))
	defer web.Close()
	worker := &Worker{client: &Client{base: web.URL, http: web.Client()}, stderr: io.Discard}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "plan"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := worker.prepareInputs(protocol.RunSpec{Inputs: map[string]protocol.Artifact{"plan": {ID: "file", Size: 3}}})(ctx, dir)
	var response *ResponseError
	if !errors.As(err, &response) || response.Retryable() || downloads.Load() != 1 {
		t.Fatalf("local error retried: %v; downloads %d", err, downloads.Load())
	}
}
