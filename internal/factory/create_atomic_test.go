package factory

import (
	"encoding/json"
	"testing"
)

func TestCreateTaskAndRetryMarkerCommitTogether(t *testing.T) {
	s, db := fixture(t)
	s.mu.Lock()
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.RequestID = "create-turn"
	foreman.Status = "running"
	s.active = foreman.ID
	s.tokens["create-token"] = foreman.ID
	_ = s.saveSession(foreman)
	s.mu.Unlock()
	if _, e = db.Exec(`CREATE TRIGGER reject_create_marker BEFORE INSERT ON factory_records WHEN NEW.kind='request' AND NEW.id GLOB 'create:*' BEGIN SELECT RAISE(ABORT,'marker persistence failed'); END`); e != nil {
		t.Fatal(e)
	}
	body := `{"title":"One task","brief":"Create exactly once"}`
	w := call(s, "POST", "tools/create_task", body, "create-token")
	if w.Code != 409 {
		t.Fatal(w.Body.String())
	}
	s.mu.Lock()
	tasks, requests := len(s.tasks), len(s.requests)
	s.mu.Unlock()
	var saved int
	if e = db.QueryRow(`SELECT COUNT(*) FROM factory_records WHERE kind='task' OR (kind='request' AND id GLOB 'create:*')`).Scan(&saved); e != nil {
		t.Fatal(e)
	}
	if tasks != 0 || requests != 0 || saved != 0 {
		t.Fatalf("partial create published: tasks=%d requests=%d saved=%d", tasks, requests, saved)
	}
	if len(fixtureWorktrees(t, s.cfg.Projects["project"].Path)) != 1 || fixtureFactoryBranches(t, s.cfg.Projects["project"].Path) != "" {
		t.Fatal("failed task persistence left an orphan workspace or branch")
	}
	if _, e = db.Exec(`DROP TRIGGER reject_create_marker`); e != nil {
		t.Fatal(e)
	}
	w = call(s, "POST", "tools/create_task", body, "create-token")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var created struct{ Task Task }
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	retry := call(s, "POST", "tools/create_task", body, "create-token")
	if retry.Code != 200 || retry.Body.String() != w.Body.String() {
		t.Fatal("retry created another task")
	}
	s.mu.Lock()
	count := len(s.tasks)
	s.active = ""
	s.mu.Unlock()
	if count != 1 {
		t.Fatal("duplicate task after retry")
	}
	if len(fixtureWorktrees(t, s.cfg.Projects["project"].Path)) != 2 {
		t.Fatal("retry left an orphan attempt workspace")
	}
	if e = db.QueryRow(`SELECT COUNT(*) FROM factory_records WHERE kind='task'`).Scan(&saved); e != nil || saved != 1 {
		t.Fatal("task records duplicated")
	}
	s.Close()
	restored, e := New(db, s.cfg, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restored.mu.Lock()
	restoredForeman := restored.sessions[foreman.ID]
	restoredForeman.Status = "running"
	restored.active = foreman.ID
	restored.tokens["create-token"] = foreman.ID
	restored.mu.Unlock()
	retry = call(restored, "POST", "tools/create_task", body, "create-token")
	if retry.Code != 200 {
		t.Fatal(retry.Body.String())
	}
	var afterRestart struct{ Task Task }
	if e = json.Unmarshal(retry.Body.Bytes(), &afterRestart); e != nil || afterRestart.Task.ID != created.Task.ID {
		t.Fatal("restart lost retry marker")
	}
	restored.mu.Lock()
	restored.active = ""
	restored.mu.Unlock()
}
