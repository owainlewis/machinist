package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func TestPullRequestInspectionUsesProjectHost(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Remote PR", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	project := s.cfg.Projects["project"]
	project.Host = "vm"
	host := config.FactoryHost{SSH: "fixture-vm"}
	s.cfg.Projects["project"] = project
	s.cfg.Hosts["vm"] = host
	task.ProjectSnapshot = project
	task.HostSnapshot = host
	task.Step = len(task.Steps)
	task.Revision = task.BaseRevision
	task.CodeApproved = task.Revision
	task.PRURL = "https://github.com/example/project/pull/1"
	s.mu.Unlock()
	localBin := t.TempDir()
	remoteBin := t.TempDir()
	controllerCalled := filepath.Join(localBin, "controller-called")
	remoteCalls := filepath.Join(remoteBin, "calls")
	localGH := "#!/bin/sh\ntouch " + shellQuote(controllerCalled) + "\nexit 99\n"
	remoteGH := "#!/bin/sh\nprintf 'GitHub warning\\n' >&2\nprintf '%s\\n' \"$*\" >> " + shellQuote(remoteCalls) + "\nprintf '%s' '{\"state\":\"OPEN\",\"headRefOid\":\"" + task.Revision + "\",\"statusCheckRollup\":[]}'\n"
	ssh := "#!/bin/sh\n[ \"$1\" = '-o' ] && [ \"$2\" = 'BatchMode=yes' ] && [ \"$3\" = 'fixture-vm' ] || exit 9\nPATH=" + shellQuote(remoteBin) + ":\"$PATH\" exec sh -c \"$4\"\n"
	for path, script := range map[string]string{filepath.Join(localBin, "gh"): localGH, filepath.Join(localBin, "ssh"): ssh, filepath.Join(remoteBin, "gh"): remoteGH} {
		if e = os.WriteFile(path, []byte(script), 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", localBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := call(s, "POST", "tasks/"+task.ID+"/refresh", `{}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, e = os.Stat(controllerCalled); !os.IsNotExist(e) {
		t.Fatal("PR inspected with controller credentials", e)
	}
	calls, e := os.ReadFile(remoteCalls)
	if e != nil || !strings.Contains(string(calls), "pr view "+task.PRURL+" --json state,mergedAt,headRefOid,statusCheckRollup") {
		t.Fatal("remote GitHub inspection arguments incorrect", e, string(calls))
	}
	s.mu.Lock()
	same := task.GitHubHead == task.Revision && task.GitHubChecksPass
	s.mu.Unlock()
	if !same {
		t.Fatal("remote PR result not applied")
	}
}

func TestCreateRetryKeyIncludesBriefAndResolvedPipeline(t *testing.T) {
	s, db := fixture(t)
	s.mu.Lock()
	s.cfg.Pipelines["alternate"] = s.cfg.Pipelines["default"]
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "running"
	foreman.RequestID = "same-turn"
	s.active = foreman.ID
	s.tokens["create"] = foreman.ID
	_ = s.saveSession(foreman)
	s.mu.Unlock()
	bodies := []string{`{"title":"Same title","brief":"First brief"}`, `{"title":"Same title","brief":"Second brief"}`, `{"title":"Same title","brief":"First brief","pipeline":"alternate"}`}
	ids := []string{}
	for _, body := range bodies {
		w := call(s, "POST", "tools/create_task", body, "create")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response struct{ Task Task }
		if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, response.Task.ID)
	}
	if ids[0] == ids[1] || ids[0] == ids[2] || ids[1] == ids[2] {
		t.Fatal("distinct creation arguments conflated")
	}
	bodies[0] = `{"title":"Same title","brief":"First brief","pipeline":"default"}`
	s.mu.Lock()
	s.active = ""
	s.mu.Unlock()
	s.Close()
	restored, e := New(db, s.cfg, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restored.mu.Lock()
	rf := restored.sessions[foreman.ID]
	rf.Status = "running"
	restored.active = rf.ID
	restored.tokens["create"] = rf.ID
	restored.mu.Unlock()
	for i, body := range bodies {
		w := call(restored, "POST", "tools/create_task", body, "create")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response struct{ Task Task }
		if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil || response.Task.ID != ids[i] {
			t.Fatal("restart lost canonical retry identity", e)
		}
	}
	restored.mu.Lock()
	count := len(restored.tasks)
	restored.active = ""
	restored.mu.Unlock()
	if count != 3 {
		t.Fatal("retry created extra tasks")
	}
}
