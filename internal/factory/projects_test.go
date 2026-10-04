package factory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func projectBody(t *testing.T, in projectInput) string {
	t.Helper()
	b, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func copyRepository(t *testing.T, source string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "repository")
	if b, e := exec.Command("git", "clone", source, dest).CombinedOutput(); e != nil {
		t.Fatalf("%s: %v", b, e)
	}
	return dest
}
func TestAddFolderProjectPersistsAndRetries(t *testing.T) {
	s, db := fixture(t)
	original := s.cfg
	path := copyRepository(t, s.cfg.Projects["project"].Path)
	in := projectInput{RequestID: "folder-one", Name: "Another project", Host: "local", Source: "folder", Path: path}
	w := call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Project struct{ ID, Host, Path, Source string }
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if response.Project.Host != "local" || response.Project.Path != path || response.Project.Source != "folder" {
		t.Fatal(w.Body.String())
	}
	retry := call(s, "POST", "projects", projectBody(t, in), "")
	if retry.Code != 200 || retry.Body.String() != w.Body.String() {
		t.Fatal(retry.Body.String())
	}
	s.Close()
	// Recreate the original startup configuration, without the browser-added entry.
	delete(original.Projects, response.Project.ID)
	restored, e := New(db, original, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if restored.cfg.Projects[response.Project.ID].Path != path {
		t.Fatal("project missing after restart")
	}
	retry = call(restored, "POST", "projects", projectBody(t, in), "")
	if retry.Code != 200 {
		t.Fatal(retry.Body.String())
	}
	// Explicit configuration wins if a stored ID is later configured by the owner.
	restored.Close()
	original.Projects[response.Project.ID] = config.FactoryProject{Name: "Configured", Path: path, Host: "local"}
	configured, e := New(db, original, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer configured.Close()
	if configured.cfg.Projects[response.Project.ID].Name != "Configured" {
		t.Fatal("stored project replaced configured project")
	}
}
func TestProjectSetupRejectsMissingOrNonRepositoryAndUnknownHost(t *testing.T) {
	s, _ := fixture(t)
	for _, in := range []projectInput{
		{RequestID: "a", Name: "Missing", Host: "local", Source: "folder", Path: filepath.Join(t.TempDir(), "missing")},
		{RequestID: "b", Name: "Not Git", Host: "local", Source: "folder", Path: t.TempDir()},
		{RequestID: "c", Name: "Unknown", Host: "missing", Source: "folder", Path: s.cfg.Projects["project"].Path},
		{RequestID: "d", Name: "Relative", Source: "folder", Path: "relative"},
		{RequestID: "e", Name: "Secret", Source: "git", Path: filepath.Join(t.TempDir(), "repo"), GitURL: "https://token@example.com/repo.git"},
	} {
		w := call(s, "POST", "projects", projectBody(t, in), "")
		if w.Code != 409 {
			t.Fatalf("%+v: %s", in, w.Body.String())
		}
	}
	if len(s.cfg.Projects) != 1 {
		t.Fatal("invalid projects saved")
	}
}
func TestGitProjectClonesOnceRestoresAndNeverReplaces(t *testing.T) {
	s, _ := fixture(t)
	origin := s.cfg.Projects["project"].Path
	url := "https://github.com/owner/repo.git"
	// Use Git's own URL rewrite to exercise clone without an external server.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+origin+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", url)
	path := filepath.Join(t.TempDir(), "clone")
	in := projectInput{RequestID: "clone-one", Name: "Clone", Host: "local", Source: "git", Path: path, GitURL: url}
	w := call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var response struct{ Project struct{ ID string } }
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	project := s.cfg.Projects[response.Project.ID]
	if project.GitHub != "owner/repo" {
		t.Fatal("clone did not infer GitHub repository")
	}
	marker := filepath.Join(path, "uncommitted.txt")
	if e := os.WriteFile(marker, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	// Clone source is a local fixture, but the stored origin remains the requested URL.
	if e := prepareProject(context.Background(), project, s.cfg.Hosts["local"]); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "keep" {
		t.Fatal("existing checkout was replaced")
	}
	// Removing this disposable checkout simulates a missing managed repository.
	if e := os.RemoveAll(path); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	task, e := s.create(response.Project.ID, "Restore", "Recreate a missing checkout", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if task.HostSnapshot.SSH != "" || task.ProjectSnapshot.Host != "local" {
		t.Fatal("host changed")
	}
	if _, e = os.Stat(filepath.Join(path, ".git")); e != nil {
		t.Fatal("checkout not restored")
	}
	existing := t.TempDir()
	keep := filepath.Join(existing, "keep.txt")
	_ = os.WriteFile(keep, []byte("keep"), 0600)
	in.RequestID = "collision"
	in.Path = existing
	w = call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	if b, e := os.ReadFile(keep); e != nil || string(b) != "keep" {
		t.Fatal("existing folder modified")
	}
	// A Git checkout with a different origin also must remain untouched.
	wrong := copyRepository(t, origin)
	in.RequestID = "wrong-origin"
	in.Path = wrong
	w = call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "different Git origin") {
		t.Fatal(w.Body.String())
	}
}
func TestRemoteProjectPinsHostAndQuotesPaths(t *testing.T) {
	s, _ := fixture(t)
	path := copyRepository(t, s.cfg.Projects["project"].Path)
	special := filepath.Join(filepath.Dir(path), "repo's folder")
	if e := os.Rename(path, special); e != nil {
		t.Fatal(e)
	}
	tools := t.TempDir()
	log := filepath.Join(tools, "ssh.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$3\" >> " + shellQuote(log) + "\n[ \"$1\" = '-o' ] && [ \"$2\" = 'BatchMode=yes' ] && [ \"$3\" = 'fixture-vm' ] || exit 9\nexec sh -c \"$4\"\n"
	if e := os.WriteFile(filepath.Join(tools, "ssh"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.cfg.Hosts["vm"] = config.FactoryHost{Name: "Build VM", SSH: "fixture-vm"}
	in := projectInput{RequestID: "remote", Name: "Remote", Host: "vm", Source: "folder", Path: special}
	w := call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var response struct{ Project struct{ ID string } }
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	s.mu.Lock()
	task, e := s.create(response.Project.ID, "Remote task", "Use the selected machine", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if task.ProjectSnapshot.Host != "vm" || task.HostSnapshot.SSH != "fixture-vm" {
		t.Fatal("task is not pinned to host")
	}
	status := call(s, "GET", "status", "", "")
	if strings.Contains(status.Body.String(), "fixture-vm") {
		t.Fatal("status exposed SSH settings")
	}
	if !strings.Contains(status.Body.String(), "Build VM") {
		t.Fatal("host name missing")
	}
}
func TestSlowProjectSetupDoesNotBlockStatusAndConcurrentRetry(t *testing.T) {
	s, _ := fixture(t)
	path := copyRepository(t, s.cfg.Projects["project"].Path)
	tools := t.TempDir()
	ready := filepath.Join(tools, "ready")
	release := filepath.Join(tools, "release")
	realGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	script := "#!/bin/sh\ntouch " + shellQuote(ready) + "\nwhile [ ! -e " + shellQuote(release) + " ]; do sleep 0.01; done\nexec " + shellQuote(realGit) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(tools, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	in := projectInput{RequestID: "slow", Name: "Slow", Host: "local", Source: "folder", Path: path}
	done := make(chan int, 1)
	go func() { done <- call(s, "POST", "projects", projectBody(t, in), "").Code }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, e = os.Stat(ready); e == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, e = os.Stat(ready); e != nil {
		t.Fatal("provisioning did not start")
	}
	status := make(chan int, 1)
	go func() { status <- call(s, "GET", "status", "", "").Code }()
	select {
	case code := <-status:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("setup blocks service state")
	}
	if w := call(s, "POST", "projects", projectBody(t, in), ""); w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	_ = os.WriteFile(release, nil, 0600)
	select {
	case code := <-done:
		if code != 201 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("setup did not finish")
	}
	if w := call(s, "POST", "projects", projectBody(t, in), ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestSlowTaskCreationReleasesLockAndRejectsStoppedForeman(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "running"
	foreman.RequestID = "turn-one"
	s.active = foreman.ID
	s.tokens["foreman-token"] = foreman.ID
	s.mu.Unlock()
	realGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	ready := filepath.Join(bin, "ready")
	release := filepath.Join(bin, "release")
	script := "#!/bin/sh\nif [ \"$1\" = worktree ]; then touch " + shellQuote(ready) + "; while [ ! -f " + shellQuote(release) + " ]; do sleep 0.01; done; fi\nexec " + shellQuote(realGit) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	done := make(chan int, 1)
	go func() {
		done <- call(s, "POST", "tools/create_task", `{"title":"Slow workspace","brief":"Wait for Git"}`, "foreman-token").Code
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e = os.Stat(ready); e == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, e = os.Stat(ready); e != nil {
		t.Fatal("workspace setup did not start")
	}
	cancelled := make(chan int, 1)
	go func() { cancelled <- call(s, "POST", "sessions/"+foreman.ID+"/cancel", `{}`, "").Code }()
	select {
	case code := <-cancelled:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("workspace setup blocked Stop")
	}
	_ = os.WriteFile(release, nil, 0600)
	select {
	case code := <-done:
		if code != 409 {
			t.Fatalf("stopped foreman created a task: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workspace setup did not finish")
	}
	s.mu.Lock()
	count := len(s.tasks)
	s.active = ""
	s.mu.Unlock()
	if count != 0 {
		t.Fatal("stale task persisted")
	}
}
func TestRemoteCancellationRetainsInterruptedOwnership(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Remote turn", "Stop safely", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	p := s.cfg.Projects["project"]
	p.Host = "vm"
	s.cfg.Projects["project"] = p
	s.cfg.Hosts["vm"] = config.FactoryHost{Name: "VM", SSH: "vm"}
	task.ProjectSnapshot = p
	task.HostSnapshot = s.cfg.Hosts["vm"]
	s.mu.Unlock()
	started := make(chan struct{}, 1)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		if r.Host == "local" {
			return "foreman-provider", nil
		}
		if r.Host != "vm" {
			t.Error("worker moved hosts")
		}
		started <- struct{}{}
		<-ctx.Done()
		return "remote-provider", nil
	})
	s.mu.Lock()
	worker, e := s.start(task)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	w := call(s, "POST", "sessions/"+worker.ID+"/cancel", `{}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"interrupted"`) {
		t.Fatal(w.Body.String())
	}
	idle(t, s)
	s.mu.Lock()
	status := worker.Status
	_, e = s.start(task)
	s.mu.Unlock()
	if status != "interrupted" || e == nil {
		t.Fatalf("remote ownership released: status=%s err=%v", status, e)
	}
	w = call(s, "POST", "sessions/"+worker.ID+"/resume", `{"confirmed_stopped":false}`, "")
	if w.Code != 409 {
		t.Fatal("remote resume skipped stopped confirmation")
	}
}

func TestProjectInfersGitHubFromOrigin(t *testing.T) {
	for _, url := range []string{"https://github.com/owner/repo.git", "ssh://git@github.com/owner/repo.git", "git@github.com:owner/repo.git"} {
		t.Run(url, func(t *testing.T) {
			s, _ := fixture(t)
			path := copyRepository(t, s.cfg.Projects["project"].Path)
			if b, e := exec.Command("git", "-C", path, "remote", "set-url", "origin", url).CombinedOutput(); e != nil {
				t.Fatalf("%s %v", b, e)
			}
			in := projectInput{RequestID: "infer", Name: "GitHub", Host: "local", Source: "folder", Path: path}
			w := call(s, "POST", "projects", projectBody(t, in), "")
			if w.Code != 201 || !strings.Contains(w.Body.String(), `"github":"owner/repo"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
	for _, url := range []string{"https://enterprise.example/owner/repo.git", "https://github.com.evil/owner/repo.git", "https://github.com/owner/repo/tree/main"} {
		if githubFromOrigin(url) != "" {
			t.Fatalf("guessed GitHub for %s", url)
		}
	}
	s, _ := fixture(t)
	path := copyRepository(t, s.cfg.Projects["project"].Path)
	_ = exec.Command("git", "-C", path, "remote", "set-url", "origin", "https://github.com/owner/repo.git").Run()
	in := projectInput{RequestID: "explicit", Name: "Explicit", Host: "local", Source: "folder", Path: path, GitHub: "other/repository"}
	w := call(s, "POST", "projects", projectBody(t, in), "")
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"github":"other/repository"`) {
		t.Fatal(w.Body.String())
	}
}

func TestGitURLRejectsCredentialsAndOptionLikeHosts(t *testing.T) {
	for _, raw := range []string{"-repository", "git@-oProxyCommand=touch:repo", "ssh://git@-host/repo", "https://token@github.com/owner/repo", "https://github.com/owner/repo?token=secret", "ssh://git:secret@github.com/owner/repo"} {
		if validGitURL(raw) {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestBrowserProjectRejectsUnsafeGitHubRepository(t *testing.T) {
	s, _ := fixture(t)
	for _, value := range []string{"owner/", "/repo", "owner/repo?x", "owner/repo#x", "owner/..", "owner/repo%2Fextra"} {
		in := projectInput{RequestID: "unsafe", Name: "Project", Host: "local", Source: "folder", Path: s.cfg.Projects["project"].Path, GitHub: value}
		w := call(s, "POST", "projects", projectBody(t, in), "")
		if w.Code != 409 || !strings.Contains(w.Body.String(), "github must be owner/repository") {
			t.Fatalf("unsafe GitHub repository accepted: %q %d %s", value, w.Code, w.Body.String())
		}
	}
}

func TestBrowserRemotePathsUsePOSIXRules(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		absolute  bool
	}{
		{"/srv/project/../repo", "/srv/repo", true},
		{`/srv/repo\literal/project`, `/srv/repo\literal/project`, true},
		{"C:/srv/project", "C:/srv/project", false},
		{`C:\srv\project`, `C:\srv\project`, false},
		{"relative/repo", "relative/repo", false},
	} {
		clean, absolute := cleanProjectPath("vm", tc.raw)
		if clean != tc.want || absolute != tc.absolute {
			t.Fatalf("remote path used controller rules: %q -> %q absolute=%v", tc.raw, clean, absolute)
		}
	}
	local := filepath.Join(t.TempDir(), "repo", "..", "project")
	clean, absolute := cleanProjectPath("local", local)
	if clean != filepath.Clean(local) || !absolute {
		t.Fatal("local path rules changed")
	}
}

func TestRemoteWorkspaceUsesPOSIXJoinAndDir(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "command")
	script := "#!/bin/sh\nprintf '%s' \"$4\" > " + shellQuote(log) + "\ncase \"$4\" in *rev-parse*) printf 'source-revision\\n';; esac\n"
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := config.FactoryProject{Host: "vm", Path: `/srv/repo\literal/project`}
	dir, branch, e := workspace(context.Background(), p, config.FactoryHost{SSH: "vm"}, "t_test")
	if e != nil {
		t.Fatal(e)
	}
	if dir != `/srv/repo\literal/.machinist-worktrees/t_test` || branch != "codex/factory-t_test" {
		t.Fatalf("remote path used controller separators: %q", dir)
	}
	command, e := os.ReadFile(log)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(command), shellQuote(dir)) {
		t.Fatal("Git command used a different remote path")
	}
}
