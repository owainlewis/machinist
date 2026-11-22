package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// LinearEndpoint is Linear's GraphQL API.
const LinearEndpoint = "https://api.linear.app/graphql"

const (
	// linearPage and linearHistory keep one query well inside Linear's
	// complexity limit.
	linearPage    = 25
	linearHistory = 20
	// maxLinearPages bounds one poll. Anything left is read on the next one.
	maxLinearPages  = 8
	maxLinearAnswer = 4 << 20
	// maxReplyPages bounds one read of replies. Replies are read whole or not
	// at all, so the cursor never moves past one that was not read.
	maxReplyPages = 40
)

// Linear reads issues given to the Machinist app from Linear's API, moves them
// between states and comments on them. It is a Tracker.
//
// An issue is given to the app when the app is its delegate, which is how
// Linear hands work to an agent while a person stays the assignee, or when the
// app is its assignee, which older workspaces still do.
type Linear struct {
	// Token is the Machinist app's Linear access token.
	Token string
	// AppUserID is the app's Linear user. Issues delegated or assigned to it
	// are work, in any team. TrackerRules decide where each one runs.
	AppUserID string
	Endpoint  string
	Client    *http.Client
}

var _ Tracker = (*Linear)(nil)

func (l *Linear) Name() string { return "linear" }

type linearIssue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Team        struct {
		Key string `json:"key"`
	} `json:"team"`
	Project *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	History struct {
		Nodes []linearChange `json:"nodes"`
	} `json:"history"`
}

// linearChange is one entry in an issue's history.
type linearChange struct {
	CreatedAt    time.Time   `json:"createdAt"`
	ActorID      string      `json:"actorId"`
	ToAssigneeID string      `json:"toAssigneeId"`
	ToDelegate   *linearUser `json:"toDelegate"`
}

// givenToApp is the part of an issue filter that matches issues delegated or
// assigned to the app.
func (l *Linear) givenToApp() string {
	return fmt.Sprintf(`or: [{delegate: {id: {eq: %[1]s}}}, {assignee: {id: {eq: %[1]s}}}]`, quote(l.AppUserID))
}

// Assignments returns the issues delegated or assigned to the app and updated
// after since, oldest first. Issues in every team are read, so one that maps
// to no repository can be told why it starts no job.
func (l *Linear) Assignments(ctx context.Context, since time.Time) ([]Assignment, error) {
	query := fmt.Sprintf(`query Assigned($after: String) {
  issues(first: %d, after: $after, orderBy: updatedAt, filter: {%s, updatedAt: {gt: %s}}) {
    nodes { id identifier title description url updatedAt team { key } project { id name }
      history(first: %d) { nodes { createdAt actorId toAssigneeId toDelegate { id } } } }
    pageInfo { hasNextPage endCursor }
  }
}`, linearPage, l.givenToApp(), quote(since.UTC().Format(time.RFC3339Nano)), linearHistory)
	var assignments []Assignment
	var after *string
	for page := 0; page < maxLinearPages; page++ {
		var answer struct {
			Issues struct {
				Nodes    []linearIssue `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		if err := l.do(ctx, query, map[string]any{"after": after}, &answer); err != nil {
			return nil, err
		}
		for _, issue := range answer.Issues.Nodes {
			assignments = append(assignments, l.assignment(issue))
		}
		if !answer.Issues.PageInfo.HasNextPage {
			break
		}
		cursor := answer.Issues.PageInfo.EndCursor
		after = &cursor
	}
	sort.SliceStable(assignments, func(a, b int) bool { return assignments[a].UpdatedAt.Before(assignments[b].UpdatedAt) })
	return assignments, nil
}

// assignment finds who gave the issue to the app: the actor of the latest
// history entry that delegated or assigned it to the app.
func (l *Linear) assignment(issue linearIssue) Assignment {
	var actor string
	var at time.Time
	for _, entry := range issue.History.Nodes {
		given := entry.ToDelegate.is(l.AppUserID) || entry.ToAssigneeID == l.AppUserID
		if given && entry.ActorID != "" && !entry.CreatedAt.Before(at) {
			actor, at = entry.ActorID, entry.CreatedAt
		}
	}
	assignment := Assignment{
		Key:        "linear:" + issue.ID,
		Issue:      issue.ID,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		Body:       issue.Description,
		URL:        issue.URL,
		Team:       issue.Team.Key,
		Actor:      actor,
		GivenAt:    at,
		UpdatedAt:  issue.UpdatedAt,
	}
	if issue.Project != nil {
		assignment.ProjectID, assignment.Project = issue.Project.ID, issue.Project.Name
	}
	return assignment
}

// Comment posts body on an issue as the app.
func (l *Linear) Comment(ctx context.Context, issue, body string) error {
	var answer struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	mutation := `mutation Comment($issue: String!, $body: String!) { commentCreate(input: {issueId: $issue, body: $body}) { success } }`
	if err := l.do(ctx, mutation, map[string]any{"issue": issue, "body": body}, &answer); err != nil {
		return err
	}
	if !answer.CommentCreate.Success {
		return errors.New("linear did not create the comment")
	}
	return nil
}

// Start moves an issue to its team's In Progress state, or the first started
// state if the team has none by that name, and says Machinist is on it.
func (l *Linear) Start(ctx context.Context, issue string) error {
	states, err := l.teamStates(ctx, issue)
	if err != nil {
		return err
	}
	state, ok := pickState(states, "started", "In Progress")
	if !ok {
		return errors.New("linear: the team has no started state")
	}
	if err := l.move(ctx, issue, state); err != nil {
		return err
	}
	return l.Comment(ctx, issue, acknowledgement)
}

// acknowledgement is Machinist's comment when it starts a job on an issue.
const acknowledgement = "Machinist is working on this. It will comment here with the pull request, or with what it found if no change is needed."

// Fail moves an issue back to its team's first unstarted state, such as Todo,
// and comments why.
func (l *Linear) Fail(ctx context.Context, issue, reason string) error {
	states, err := l.teamStates(ctx, issue)
	if err != nil {
		return err
	}
	state, ok := pickState(states, "unstarted", "Todo")
	if !ok {
		return errors.New("linear: the team has no unstarted state")
	}
	if err := l.move(ctx, issue, state); err != nil {
		return err
	}
	return l.Comment(ctx, issue, "Machinist's job failed: "+reason+"\n\nReply here to tell it what to change, and it will run again.")
}

// Return moves an issue back to its team's first unstarted state, such as
// Todo, and comments.
func (l *Linear) Return(ctx context.Context, issue, comment string) error {
	states, err := l.teamStates(ctx, issue)
	if err != nil {
		return err
	}
	state, ok := pickState(states, "unstarted", "Todo")
	if !ok {
		return errors.New("linear: the team has no unstarted state")
	}
	if err := l.move(ctx, issue, state); err != nil {
		return err
	}
	return l.Comment(ctx, issue, comment)
}

// Replies returns comments by anyone but the app on issues delegated or
// assigned to the app, created after since, oldest first. Only those on an
// issue with a job are used.
func (l *Linear) Replies(ctx context.Context, since, until time.Time) ([]Reply, error) {
	window := fmt.Sprintf("gt: %s", quote(since.UTC().Format(time.RFC3339Nano)))
	if !until.IsZero() {
		window += fmt.Sprintf(", lte: %s", quote(until.UTC().Format(time.RFC3339Nano)))
	}
	query := fmt.Sprintf(`query Replies($after: String) {
  comments(first: %d, after: $after, orderBy: createdAt, filter: {createdAt: {%s}, user: {id: {neq: %s}}, issue: {%s}}) {
    nodes { id body createdAt user { id } issue { id } }
    pageInfo { hasNextPage endCursor }
  }
}`, linearPage, window, quote(l.AppUserID), l.givenToApp())
	var replies []Reply
	var after *string
	for page := 0; ; page++ {
		if page == maxReplyPages {
			return nil, fmt.Errorf("linear: more than %d replies since %s: %w", maxReplyPages*linearPage, since.Format(time.RFC3339), ErrTooManyReplies)
		}
		var answer struct {
			Comments struct {
				Nodes []struct {
					ID        string    `json:"id"`
					Body      string    `json:"body"`
					CreatedAt time.Time `json:"createdAt"`
					User      *struct {
						ID string `json:"id"`
					} `json:"user"`
					Issue struct {
						ID string `json:"id"`
					} `json:"issue"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"comments"`
		}
		if err := l.do(ctx, query, map[string]any{"after": after}, &answer); err != nil {
			return nil, err
		}
		for _, comment := range answer.Comments.Nodes {
			// A comment with no user came from an integration, not a person.
			if comment.User == nil || comment.User.ID == l.AppUserID || strings.TrimSpace(comment.Body) == "" {
				continue
			}
			replies = append(replies, Reply{Key: "linear:" + comment.Issue.ID, ID: comment.ID, Issue: comment.Issue.ID, Author: comment.User.ID, Body: comment.Body, CreatedAt: comment.CreatedAt})
		}
		if !answer.Comments.PageInfo.HasNextPage {
			break
		}
		cursor := answer.Comments.PageInfo.EndCursor
		after = &cursor
	}
	sort.SliceStable(replies, func(a, b int) bool { return replies[a].CreatedAt.Before(replies[b].CreatedAt) })
	return replies, nil
}

// Finish moves an issue to its team's Done state, or the first completed state
// if the team has none by that name.
func (l *Linear) Finish(ctx context.Context, issue string) error {
	states, err := l.teamStates(ctx, issue)
	if err != nil {
		return err
	}
	state, ok := pickState(states, "completed", "Done")
	if !ok {
		return errors.New("linear: the team has no completed state")
	}
	return l.move(ctx, issue, state)
}

// States reads whether each issue is still delegated or assigned to the app,
// and started. Removing the app as delegate counts as unassigning it.
func (l *Linear) States(ctx context.Context, issues []string) (map[string]IssueState, error) {
	states := map[string]IssueState{}
	for len(issues) > 0 {
		batch := issues[:min(len(issues), linearPage)]
		issues = issues[len(batch):]
		var answer struct {
			Issues struct {
				Nodes []struct {
					ID       string      `json:"id"`
					Assignee *linearUser `json:"assignee"`
					Delegate *linearUser `json:"delegate"`
					State    struct {
						Type string `json:"type"`
					} `json:"state"`
				} `json:"nodes"`
			} `json:"issues"`
		}
		query := `query States($ids: [ID!]) { issues(first: 50, filter: {id: {in: $ids}}) { nodes { id assignee { id } delegate { id } state { type } } } }`
		if err := l.do(ctx, query, map[string]any{"ids": batch}, &answer); err != nil {
			return nil, err
		}
		for _, node := range answer.Issues.Nodes {
			states[node.ID] = IssueState{
				Assigned: node.Delegate.is(l.AppUserID) || node.Assignee.is(l.AppUserID),
				Active:   node.State.Type == "started",
			}
		}
	}
	return states, nil
}

type linearUser struct {
	ID string `json:"id"`
}

func (u *linearUser) is(id string) bool { return u != nil && u.ID == id }

type linearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

func (l *Linear) teamStates(ctx context.Context, issue string) ([]linearState, error) {
	var answer struct {
		Issue *struct {
			Team struct {
				States struct {
					Nodes []linearState `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	query := `query TeamStates($issue: String!) { issue(id: $issue) { team { states { nodes { id name type position } } } } }`
	if err := l.do(ctx, query, map[string]any{"issue": issue}, &answer); err != nil {
		return nil, err
	}
	if answer.Issue == nil {
		return nil, errors.New("linear: the issue is gone")
	}
	return answer.Issue.Team.States.Nodes, nil
}

// pickState chooses the state of a type with the given name, or else the
// first state of that type on the team's board.
func pickState(states []linearState, kind, name string) (string, bool) {
	var first *linearState
	for i, state := range states {
		if state.Type != kind {
			continue
		}
		if strings.EqualFold(state.Name, name) {
			return state.ID, true
		}
		if first == nil || state.Position < first.Position {
			first = &states[i]
		}
	}
	if first == nil {
		return "", false
	}
	return first.ID, true
}

func (l *Linear) move(ctx context.Context, issue, state string) error {
	var answer struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	mutation := `mutation Move($issue: String!, $state: String!) { issueUpdate(id: $issue, input: {stateId: $state}) { success } }`
	if err := l.do(ctx, mutation, map[string]any{"issue": issue, "state": state}, &answer); err != nil {
		return err
	}
	if !answer.IssueUpdate.Success {
		return errors.New("linear did not update the issue")
	}
	return nil
}

func quote(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (l *Linear) do(ctx context.Context, query string, variables map[string]any, into any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	endpoint := l.Endpoint
	if endpoint == "" {
		endpoint = LinearEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	// A personal API key is sent as it is. An app's OAuth token is a bearer token.
	if strings.HasPrefix(l.Token, "lin_api_") {
		request.Header.Set("Authorization", l.Token)
	} else {
		request.Header.Set("Authorization", "Bearer "+l.Token)
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLinearAnswer))
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("linear: %w", ErrRateLimited)
	}
	var answer struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return fmt.Errorf("linear: status %d: the answer is not JSON", response.StatusCode)
	}
	if len(answer.Errors) > 0 {
		first := answer.Errors[0]
		if first.Extensions.Code == "RATELIMITED" {
			return fmt.Errorf("linear: %w", ErrRateLimited)
		}
		return fmt.Errorf("linear: %s", first.Message)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: status %d", response.StatusCode)
	}
	return json.Unmarshal(answer.Data, into)
}
