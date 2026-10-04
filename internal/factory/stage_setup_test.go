package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func TestStageInstructionsRespectDesignApproval(t *testing.T) {
	design := stageInstructions("Design")
	if !strings.Contains(design, "Planning only") || !strings.Contains(design, "Do not change workspace files or create commits") || !strings.Contains(design, "human design approval") || strings.Contains(design, "Commit implementation before reporting") {
		t.Fatal(design)
	}
	if !strings.Contains(stageInstructions("Build"), "Commit implementation before reporting") {
		t.Fatal("build commit instruction missing")
	}
	if strings.Contains(stageInstructions("Review"), "Commit implementation before reporting") {
		t.Fatal("review instructed to commit")
	}
}

func TestDeliveryWithoutGitHubStaysInReview(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Delivery", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Step = len(task.Steps)
	task.Stage = "Review"
	task.Status = "active"
	task.Revision = task.BaseRevision
	task.CodeApproved = task.Revision
	p := s.cfg.Projects["project"]
	p.GitHub = ""
	s.cfg.Projects["project"] = p
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "running"
	s.active = foreman.ID
	s.tokens["delivery"] = foreman.ID
	s.mu.Unlock()
	w := call(s, "POST", "tools/send_message", `{"task_id":"`+task.ID+`","message":"Publish","request_id":"publish-once"}`, "delivery")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "no GitHub repository configured") {
		t.Fatal(w.Code, w.Body.String())
	}
	s.mu.Lock()
	same := task.Stage == "Review" && task.PRURL == "" && len(s.queue) == 0 && len(s.sessions) == 1
	s.mu.Unlock()
	if !same {
		t.Fatal("unsupported delivery queued a worker or moved the task")
	}
}

func TestLocalProjectSetupDoesNotNeedPOSIXShell(t *testing.T) {
	s, _ := fixture(t)
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	if e = os.Symlink(git, filepath.Join(bin, "git")); e != nil {
		t.Skip("cannot link Git executable", e)
	}
	link := filepath.Join(t.TempDir(), "repository-link")
	if e = os.Symlink(s.cfg.Projects["project"].Path, link); e != nil {
		t.Skip("cannot link repository", e)
	}
	t.Setenv("PATH", bin)
	if _, e = exec.LookPath("sh"); e == nil {
		t.Fatal("test PATH unexpectedly contains sh")
	}
	p := config.FactoryProject{Host: "local", Source: "folder", Path: link}
	if e = prepareProject(context.Background(), p, config.FactoryHost{}); e != nil {
		t.Fatal("native local setup failed without sh", e)
	}
	p.Path = filepath.Join(link, "nested")
	if e = os.Mkdir(p.Path, 0700); e != nil {
		t.Fatal(e)
	}
	if e = prepareProject(context.Background(), p, config.FactoryHost{}); e == nil || !strings.Contains(e.Error(), "root folder") {
		t.Fatal("nested folder accepted", e)
	}
}
