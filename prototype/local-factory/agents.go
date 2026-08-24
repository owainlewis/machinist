package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type agentRunner struct {
	config       loadedConfig
	store        *store
	github       githubClient
	githubWrites bool
	executable   string
	authToken    string
}

func (r *agentRunner) runWork(ctx context.Context, id string) error {
	item, err := r.store.get(id)
	if err != nil {
		return err
	}
	workspace, branch, err := prepareWorkspace(ctx, r.config, item)
	if err != nil {
		return err
	}
	item, err = r.store.update(id, func(item *work) error {
		now := time.Now().UTC()
		item.State = stateRunning
		item.StartedAt = now
		item.Workspace = workspace
		item.Branch = branch
		item.ActiveRole = "foreman"
		item.Events = append(item.Events, event{At: now, Message: "foreman started"})
		return nil
	})
	if err != nil {
		return err
	}

	foremanName := r.config.Config.Roles.Foreman
	prompt := r.config.Prompts[foremanName] + "\n\n" + r.foremanContext(item)
	output, err := r.runAgent(ctx, foremanName, workspace, prompt, "foreman", id, r.workToken(item))
	_ = r.store.artifact(id, "foreman.md", output)
	if err != nil {
		return err
	}
	latest, err := r.store.get(id)
	if err != nil {
		return err
	}
	if latest.State == stateRunning {
		return errors.New("foreman stopped without calling finish or block")
	}
	return nil
}

func (r *agentRunner) delegate(ctx context.Context, id, role string) ([]byte, error) {
	item, err := r.store.get(id)
	if err != nil {
		return nil, err
	}
	if item.State != stateRunning {
		return nil, fmt.Errorf("work is %s, not running", item.State)
	}

	var agentName, directory, artifact string
	switch role {
	case "plan":
		agentName, directory, artifact = r.config.Config.Roles.Plan, item.Workspace, "plan.md"
	case "build":
		agentName, directory, artifact = r.config.Config.Roles.Build, item.Workspace, "build.md"
	case "verify":
		if item.VerifyRuns >= r.config.Config.MaxRevisions+1 {
			return nil, fmt.Errorf("verification limit reached after %d runs", item.VerifyRuns)
		}
		sha, err := checkpoint(ctx, item.Workspace, fmt.Sprintf("feat: address %s#%d", item.Issue.Repository, item.Issue.Number))
		if err != nil {
			return nil, fmt.Errorf("checkpoint build: %w", err)
		}
		directory, err = prepareVerificationWorkspace(ctx, r.config, item, sha)
		if err != nil {
			return nil, err
		}
		agentName, artifact = r.config.Config.Roles.Verify, "review.md"
		item.HeadSHA = sha
	default:
		return nil, errors.New("role must be plan, build, or verify")
	}

	item, err = r.store.update(id, func(current *work) error {
		current.ActiveRole = role
		if role == "verify" {
			current.HeadSHA = item.HeadSHA
			current.VerifyRuns++
			current.VerifiedSHA = ""
		}
		current.Events = append(current.Events, event{At: time.Now().UTC(), Message: role + " agent started"})
		return nil
	})
	if err != nil {
		if role == "verify" {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = removeVerificationWorkspace(cleanupContext, r.config, item, directory)
			cancel()
		}
		return nil, err
	}
	prompt := r.config.Prompts[agentName] + "\n\n" + r.roleContext(item, role, directory)
	output, runErr := r.runAgent(ctx, agentName, directory, prompt, role, id, "")
	if role == "verify" {
		if verifyErr := ensureExactHead(ctx, directory, item.HeadSHA); verifyErr != nil && runErr == nil {
			runErr = verifyErr
		}
		if runErr == nil {
			if _, verdictErr := verificationVerdict(output); verdictErr != nil {
				runErr = verdictErr
			}
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := removeVerificationWorkspace(cleanupContext, r.config, item, directory)
		cancel()
		if cleanupErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
		}
	}
	if artifactErr := r.store.artifact(id, artifact, output); artifactErr != nil && runErr == nil {
		runErr = artifactErr
	}
	_, stateErr := r.store.update(id, func(current *work) error {
		current.ActiveRole = "foreman"
		message := role + " agent completed"
		if runErr != nil {
			if role == "verify" {
				current.VerifiedSHA = ""
			}
			message = role + " agent failed: " + runErr.Error()
		} else if role == "verify" {
			verdict, _ := verificationVerdict(output)
			if verdict == "PASS" {
				current.VerifiedSHA = current.HeadSHA
				message = "verify agent passed candidate"
			} else {
				current.VerifiedSHA = ""
				message = "verify agent requested revision"
			}
		}
		current.Events = append(current.Events, event{At: time.Now().UTC(), Message: message})
		return nil
	})
	if stateErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("persist %s completion: %w", role, stateErr))
	}
	return output, runErr
}

func verificationVerdict(output []byte) (string, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "", errors.New("verifier returned an empty report")
	}
	firstLine := trimmed
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	switch strings.TrimSpace(firstLine) {
	case "Verdict: PASS":
		return "PASS", nil
	case "Verdict: REVISE":
		return "REVISE", nil
	default:
		return "", errors.New("verifier report must begin with Verdict: PASS or Verdict: REVISE")
	}
}

func (r *agentRunner) runAgent(ctx context.Context, name, directory, prompt, role, id, workToken string) ([]byte, error) {
	agent := r.config.Config.Agents[name]
	var command *exec.Cmd
	switch agent.Runtime {
	case "command":
		args := append([]string(nil), agent.Command[1:]...)
		command = exec.CommandContext(ctx, agent.Command[0], args...)
	case "codex":
		args := []string{"exec", "-C", directory, "--ephemeral", "--color", "never", "--sandbox", "read-only"}
		if role == "build" || role == "verify" {
			args[len(args)-1] = "workspace-write"
		}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		args = append(args, "-")
		command = exec.CommandContext(ctx, "codex", args...)
	case "claude":
		args := []string{"--print", "--safe-mode", "--no-session-persistence", "--output-format", "text", "--permission-mode", "dontAsk"}
		if role == "foreman" {
			args = append(args, "--tools", "Bash", "--allowedTools", "Bash("+r.executable+" internal *)")
		} else if role == "plan" {
			args = append(args, "--tools", "Read,Glob,Grep", "--allowedTools", "Read,Glob,Grep")
		} else {
			args = append(args, "--tools", "Read,Edit,Write,Glob,Grep,Bash", "--allowedTools", "Read,Edit,Write,Glob,Grep,Bash")
		}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		command = exec.CommandContext(ctx, "claude", args...)
	default:
		return nil, fmt.Errorf("unsupported runtime %q", agent.Runtime)
	}
	command.Dir = directory
	configureAgentCommand(command)
	command.Stdin = strings.NewReader(prompt)
	command.Env = append(withoutFactoryEnvironment(os.Environ()),
		"FACTORY_CONFIG="+r.config.Path,
		"FACTORY_EXECUTABLE="+r.executable,
		"FACTORY_WORK_ID="+id,
		"FACTORY_ROLE="+role,
		"FACTORY_WORKSPACE="+directory,
	)
	if role == "foreman" {
		command.Env = append(command.Env, "FACTORY_AUTH_TOKEN="+workToken)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	cleanupErr := cleanupAgentCommand(command)
	if runErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = runErr.Error()
		}
		if cleanupErr != nil {
			message += "; process cleanup: " + cleanupErr.Error()
		}
		return stdout.Bytes(), fmt.Errorf("agent %q: %s", name, message)
	}
	if cleanupErr != nil {
		return stdout.Bytes(), fmt.Errorf("agent %q process cleanup: %w", name, cleanupErr)
	}
	return stdout.Bytes(), nil
}

func (r *agentRunner) workToken(item work) string {
	mac := hmac.New(sha256.New, []byte(r.authToken))
	_, _ = fmt.Fprintf(mac, "%s\x00%d", item.ID, item.Attempt)
	return hex.EncodeToString(mac.Sum(nil))
}

func withoutFactoryEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, "FACTORY_") {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func configureAgentCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		processGroup := -command.Process.Pid
		err := syscall.Kill(processGroup, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err == nil {
			go func() {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				<-timer.C
				_ = syscall.Kill(processGroup, syscall.SIGKILL)
			}()
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
}

func cleanupAgentCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	processGroup := -command.Process.Pid
	err := syscall.Kill(processGroup, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	err = syscall.Kill(processGroup, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (r *agentRunner) foremanContext(item work) string {
	command := fmt.Sprintf("%s internal --config %s --work %s", shellQuote(r.executable), shellQuote(r.config.Path), shellQuote(item.ID))
	return fmt.Sprintf(`## Assignment

Work ID: %s
Issue: %s#%d
Title: %s

You can act only by running these commands with Bash:

  %s delegate plan
  %s publish-plan
  %s delegate build
  %s delegate verify
  %s finish
  %s block "reason"

The delegate commands are synchronous. Their stdout is the child's natural-language report. Read it and decide what to do next.`, item.ID, item.Issue.Repository, item.Issue.Number, item.Issue.Title, command, command, command, command, command, command)
}

func (r *agentRunner) roleContext(item work, role, directory string) string {
	parts := []string{renderIssue(item.Issue)}
	if plan, err := r.store.readArtifact(item.ID, "plan.md"); err == nil && role != "plan" {
		parts = append(parts, "# Current plan\n\n"+string(plan))
	}
	if review, err := r.store.readArtifact(item.ID, "review.md"); err == nil && role == "build" {
		parts = append(parts, "# Latest verification report\n\n"+string(review))
	}
	verificationRun := item.VerifyRuns
	if role != "verify" {
		verificationRun++
	}
	parts = append(parts, "# Run context\n\nRole: "+role+"\nCheckout: "+directory+"\nVerification run: "+strconv.Itoa(verificationRun))
	return strings.Join(parts, "\n\n")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (r *agentRunner) publishPlan(ctx context.Context, id string) error {
	item, err := r.store.get(id)
	if err != nil {
		return err
	}
	if item.State != stateRunning {
		return fmt.Errorf("work is %s, not running", item.State)
	}
	plan, err := r.store.readArtifact(id, "plan.md")
	if err != nil {
		return errors.New("planner has not produced plan.md")
	}
	if r.githubWrites {
		currentIssue, err := r.github.Issue(ctx, item.Issue.Repository, item.Issue.Number)
		if err != nil {
			return fmt.Errorf("refresh issue before publishing plan: %w", err)
		}
		body, err := managedPlanBody(currentIssue.Body, string(plan))
		if err != nil {
			return fmt.Errorf("prepare managed issue plan: %w", err)
		}
		if err := r.github.UpdateIssueBody(ctx, currentIssue, body); err != nil {
			return err
		}
		_, err = r.store.event(id, "issue body updated with plan")
		return err
	}
	_, err = r.store.event(id, "dry run: plan retained locally; issue was not changed")
	return err
}

func (r *agentRunner) finish(ctx context.Context, id string) error {
	item, err := r.store.get(id)
	if err != nil {
		return err
	}
	if item.State != stateRunning {
		return fmt.Errorf("work is %s, not running", item.State)
	}
	if item.VerifyRuns == 0 || item.VerifiedSHA == "" {
		return errors.New("cannot finish before a successful verification in this attempt")
	}
	if err := ensureClean(ctx, item.Workspace); err != nil {
		return fmt.Errorf("candidate changed after verification: %w", err)
	}
	shaOutput, err := commandOutput(ctx, item.Workspace, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	sha := strings.TrimSpace(string(shaOutput))
	if sha != item.VerifiedSHA {
		return fmt.Errorf("candidate changed after verification: verified %s, current %s", item.VerifiedSHA, sha)
	}
	prURL := ""
	if r.githubWrites {
		repository, err := repositoryFor(r.config, item.Issue.Repository)
		if err != nil {
			return err
		}
		baseBranch := strings.TrimPrefix(repository.BaseRef, "origin/")
		if err := pushBranch(ctx, item.Workspace, item.Branch, item.Issue.Repository); err != nil {
			return err
		}
		plan, _ := r.store.readArtifact(id, "plan.md")
		review, _ := r.store.readArtifact(id, "review.md")
		body := fmt.Sprintf("Closes #%d\n\n## Plan\n\n%s\n\n## Verification\n\n%s", item.Issue.Number, plan, review)
		prURL, err = r.github.OpenDraftPR(ctx, item.Issue, item.Branch, baseBranch, item.Issue.Title, body)
		if err != nil {
			return err
		}
	}
	_, err = r.store.update(id, func(current *work) error {
		now := time.Now().UTC()
		current.State = stateReady
		current.ActiveRole = ""
		current.HeadSHA = sha
		current.PRURL = prURL
		current.CompletedAt = now
		message := "dry run ready; local branch retained"
		if r.githubWrites {
			message = "draft pull request opened"
		}
		current.Events = append(current.Events, event{At: now, Message: message})
		return nil
	})
	return err
}

func (r *agentRunner) block(ctx context.Context, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("block reason is required")
	}
	item, err := r.store.get(id)
	if err != nil {
		return err
	}
	if item.State != stateRunning {
		return fmt.Errorf("work is %s, not running", item.State)
	}
	if r.githubWrites {
		if err := r.github.CommentIssue(ctx, item.Issue, "Factory blocked this ticket:\n\n"+reason); err != nil {
			return err
		}
	}
	_, err = r.store.update(id, func(item *work) error {
		now := time.Now().UTC()
		item.State = stateBlocked
		item.ActiveRole = ""
		item.Failure = reason
		item.CompletedAt = now
		item.Events = append(item.Events, event{At: now, Message: "blocked: " + reason})
		return nil
	})
	return err
}

func managedPlanBody(issueBody, plan string) (string, error) {
	const start = "<!-- factory-plan:start -->"
	const end = "<!-- factory-plan:end -->"
	if strings.Contains(plan, start) || strings.Contains(plan, end) {
		return "", errors.New("plan contains reserved Factory plan markers")
	}
	block := start + "\n## Factory plan\n\n" + strings.TrimSpace(plan) + "\n" + end
	startCount := strings.Count(issueBody, start)
	endCount := strings.Count(issueBody, end)
	if startCount == 0 && endCount == 0 {
		separator := ""
		if issueBody != "" && !strings.HasSuffix(issueBody, "\n\n") {
			separator = "\n\n"
			if strings.HasSuffix(issueBody, "\n") {
				separator = "\n"
			}
		}
		return issueBody + separator + block + "\n", nil
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("issue body has incomplete or duplicate Factory plan markers")
	}
	startIndex := strings.Index(issueBody, start)
	endIndex := strings.Index(issueBody, end)
	if endIndex < startIndex {
		return "", errors.New("issue body has Factory plan markers in the wrong order")
	}
	endIndex += len(end)
	return issueBody[:startIndex] + block + issueBody[endIndex:], nil
}

func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
