package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func validPR(raw, repo string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || parts[0]+"/"+parts[1] != repo {
		return false
	}
	n, e := strconv.Atoi(parts[3])
	return e == nil && n > 0
}
func (s *Service) refresh(t *Task) error {
	if e := s.validateTask(t); e != nil {
		return e
	}
	if t.PRURL == "" {
		return errors.New("no linked pull request")
	}
	if !validPR(t.PRURL, s.cfg.Projects[t.ProjectID].GitHub) {
		return errors.New("invalid project pull request")
	}
	pr, e := s.readPRSnapshot(context.Background(), t, t.PRURL)
	if e != nil {
		return e
	}
	return s.applyPR(t, pr)
}

type githubPR struct {
	State             string
	MergedAt          *string
	HeadRefOID        string
	StatusCheckRollup []struct{ Status, Conclusion, State string }
}

func readPR(parent context.Context, raw string) (githubPR, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "gh", "pr", "view", raw, "--json", "state,mergedAt,headRefOid,statusCheckRollup").Output()
	if e != nil {
		return githubPR{}, fmt.Errorf("could not read GitHub: %w", e)
	}
	var pr githubPR
	if e = json.Unmarshal(out, &pr); e != nil {
		return githubPR{}, e
	}
	return pr, nil
}
func (s *Service) applyPR(t *Task, pr githubPR) error {
	if s.taskBusy(t.ID) {
		return errors.New("task has active or interrupted ownership; recover it before applying pull request changes")
	}
	t.ObservedAt = now()
	oldHead := t.GitHubHead
	t.GitHubHead = pr.HeadRefOID
	t.GitHubChecksPass = true
	for _, c := range pr.StatusCheckRollup {
		if c.Status != "" && c.Status != "COMPLETED" {
			t.GitHubChecksPass = false
		}
		if c.Conclusion != "" && c.Conclusion != "SUCCESS" && c.Conclusion != "NEUTRAL" && c.Conclusion != "SKIPPED" {
			t.GitHubChecksPass = false
		}
		if c.State != "" && c.State != "SUCCESS" {
			t.GitHubChecksPass = false
		}
	}
	t.GitHubError = ""
	if pr.HeadRefOID != t.Revision {
		t.CodeApproved = ""
		t.Review = ""
		t.Activity = "Pull request changed. Submit the new revision for review."
		if oldHead != pr.HeadRefOID {
			if e := s.repair(t, "Pull request HEAD changed to "+pr.HeadRefOID+". Inspect the task workspace, reconcile this revision and rerun checks and review."); e != nil {
				return e
			}
			s.notify(t.ProjectID, "Pull request changed for task "+t.ID+". Inspect task and send the builder the revision feedback.")
		}
		return s.saveTask(t)
	}
	if pr.State == "MERGED" && pr.MergedAt != nil && t.CodeApproved == t.Revision && t.Step >= len(t.Steps) && t.GitHubChecksPass {
		t.Stage = "Done"
		t.Status = "done"
		t.Activity = "Merged"
	} else if pr.State == "CLOSED" {
		t.Activity = "Pull request closed without merge"
	} else {
		t.Activity = "Waiting for merge"
	}
	return s.saveTask(t)
}

// Observe checks only linked unfinished pull requests. It never starts coding,
// publishes, or merges. The caller owns cancellation and runs one observer.
func (s *Service) Observe(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		keys := []string{}
		for key, t := range s.tasks {
			if t.PRURL != "" && t.Status != "done" && t.Status != "cancelled" {
				keys = append(keys, key)
			}
		}
		s.mu.Unlock()
		for _, key := range keys {
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			t := s.tasks[key]
			if s.closed || t == nil || t.Status == "done" || t.Status == "cancelled" || s.taskBusy(t.ID) {
				s.mu.Unlock()
				continue
			}
			pr, e := s.readPRSnapshot(ctx, t, t.PRURL)
			if !s.closed && !errors.Is(e, errStalePR) && !s.taskBusy(t.ID) {
				if e != nil {
					t.GitHubError = e.Error()
					_ = s.saveTask(t)
				} else {
					_ = s.applyPR(t, pr)
				}
				s.signal()
			}
			s.mu.Unlock()
		}
	}
}
func (s *Service) taskBusy(task string) bool {
	for _, v := range s.sessions {
		if v.TaskID == task && (v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" || v.Status == "interrupted") {
			return true
		}
	}
	return false
}

var errStalePR = errors.New("task changed while reading GitHub; reload before continuing")

// The caller holds mu. Keep network waits outside it, then reject stale results.
func (s *Service) readPRSnapshot(ctx context.Context, t *Task, raw string) (githubPR, error) {
	id, version, step, status, revision, linked := t.ID, t.Version, t.Step, t.Status, t.Revision, t.PRURL
	s.mu.Unlock()
	pr, err := readPR(ctx, raw)
	s.mu.Lock()
	if s.closed || s.tasks[id] != t || t.Version != version || t.Step != step || t.Status != status || t.Revision != revision || t.PRURL != linked {
		return githubPR{}, errStalePR
	}
	return pr, err
}
