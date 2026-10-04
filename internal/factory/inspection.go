package factory

import (
	"encoding/json"
	"sort"
	"strings"
)

// Inspection stays below the MCP transport limit, even with long check logs.
// A targeted inspection provides context for tasks omitted from a large list.
func (s *Service) inspectTasks(session *Session, taskID string) map[string]any {
	candidates := []*Task{}
	for _, t := range s.tasks {
		if t.ProjectID == session.ProjectID && (session.isForeman() || session.TaskID == t.ID) && (taskID == "" || taskID == t.ID) {
			candidates = append(candidates, t)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		archived := func(t *Task) bool { return t.Status == "done" || t.Status == "cancelled" }
		if archived(candidates[i]) != archived(candidates[j]) {
			return !archived(candidates[i])
		}
		if candidates[i].CreatedAt != candidates[j].CreatedAt {
			return candidates[i].CreatedAt > candidates[j].CreatedAt
		}
		return candidates[i].ID < candidates[j].ID
	})
	tasks := []any{}
	budget := 512 << 10
	for _, t := range candidates {
		truncated := false
		clip := func(v string, limit int) string {
			if len(v) <= limit {
				return v
			}
			truncated = true
			return strings.ToValidUTF8(v[:limit], "") + "\n[truncated]"
		}
		checks := []Check{}
		start := 0
		if len(t.Checks) > 8 {
			start = len(t.Checks) - 8
			truncated = true
		}
		for _, c := range t.Checks[start:] {
			c.Name = clip(c.Name, 512)
			c.StepID = clip(c.StepID, 512)
			c.Revision = clip(c.Revision, 512)
			c.Output = clip(c.Output, 4096)
			checks = append(checks, c)
		}
		summary := s.summarize(t)
		result := struct {
			taskSummary
			Step             int     `json:"step"`
			Design           string  `json:"design"`
			Review           string  `json:"review"`
			Revision         string  `json:"revision"`
			PRURL            string  `json:"pr_url"`
			Checks           []Check `json:"checks"`
			DetailsTruncated bool    `json:"details_truncated"`
		}{summary, t.Step, clip(t.Design, 8192), clip(t.Review, 4096), clip(t.Revision, 512), clip(t.PRURL, 1024), checks, false}
		result.DetailsTruncated = truncated
		encoded, _ := json.Marshal(result)
		if len(encoded)+1 > budget {
			break
		}
		budget -= len(encoded) + 1
		tasks = append(tasks, result)
	}
	return map[string]any{"tasks": tasks, "tasks_truncated": len(tasks) < len(candidates), "total_tasks": len(candidates)}
}

// Mutation acknowledgements contain routing/state fields, never saved artifacts.
// Detailed results remain available through bounded inspect_tasks.
func (s *Service) toolTask(t *Task) any {
	if t == nil {
		return nil
	}
	return struct {
		taskSummary
		Pipeline string `json:"pipeline"`
		Repairs  int    `json:"repairs"`
	}{s.summarize(t), summaryText(t.Pipeline), t.Repairs}
}
func toolSession(v *Session) *Session {
	if v == nil {
		return nil
	}
	copySession := *v
	copySession.Error = summaryText(v.Error)
	return &copySession
}
