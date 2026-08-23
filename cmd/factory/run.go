package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/factoryclient"
	"github.com/owainlewis/factory/internal/phase"
	"github.com/owainlewis/factory/internal/protocol"
)

const runUsage = `factory run dispatches a phase against an issue or a repository.

Usage:
  factory run <work> --phase <name> [flags]
  factory run --repo <owner/name> --phase <name> [flags]

Examples:
  factory run 412 --repo acme/api --phase build
  factory run https://github.com/acme/api/issues/412 --phase build
  factory run acme/api#412 --phase verify
  factory run --repo acme/api --phase audit
  factory run 412 --repo acme/api --phase build --dry-run
`

func runCommand(ctx context.Context, arguments []string) error {
	work, arguments := leadingArgument(arguments)
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	set.Usage = func() {
		fmt.Fprint(os.Stderr, runUsage, "\nFlags:\n")
		set.PrintDefaults()
	}
	var (
		repo      = set.String("repo", "", "repository as owner/name, or a bare name with --owner")
		owner     = set.String("owner", os.Getenv("FACTORY_OWNER"), "default repository owner")
		phaseName = set.String("phase", "", "phase to run")
		phaseList = set.String("phases", "", "comma-separated phases to run in order")
		criteria  = set.String("criteria", "", "acceptance criteria for this run")
		runtime   = set.String("runtime", "", "override the phase runtime (pi, codex, claude-code)")
		dryRun    = set.Bool("dry-run", false, "render the prompt and dispatch nothing")
		address   = serverFlag(set)
		format    = formatFlag(set)
	)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if err := checkFormat(*format); err != nil {
		return err
	}

	names, err := selectedPhases(*phaseName, *phaseList)
	if err != nil {
		return err
	}
	if work == "" {
		work = set.Arg(0)
	}
	target, err := parseTarget(work, *repo, *owner)
	if err != nil {
		return err
	}

	loaded, err := phase.Load(repositoryRoot(), names[0])
	if err != nil {
		return err
	}

	var issue *issueDetail
	if target.Number > 0 {
		detail, err := fetchIssue(ctx, target)
		if err != nil {
			return err
		}
		issue = &detail
	}

	title := loaded.Name + " " + target.Reference()
	if issue != nil {
		title = issue.Title
	}

	context := phase.Context{
		Run: map[string]string{
			"repo":     target.Slug(),
			"phase":    loaded.Name,
			"title":    title,
			"criteria": *criteria,
			"body":     "",
		},
	}
	if issue != nil {
		context.Run["body"] = issue.Body
		context.Issue = map[string]string{
			"identifier": target.Reference(),
			"number":     fmt.Sprint(target.Number),
			"title":      issue.Title,
			"body":       issue.Body,
			"url":        issue.URL,
			"state":      issue.State,
			"labels":     issue.labelNames(),
		}
	}

	prompt, err := loaded.Render(context)
	if err != nil {
		return err
	}

	if *dryRun {
		return showDryRun(*format, loaded, target, prompt)
	}

	selected, err := resolveRuntime(*runtime, loaded.Runtime)
	if err != nil {
		return err
	}

	client, err := factoryclient.New(*address)
	if err != nil {
		return err
	}
	repository, err := client.EnsureRepository(ctx, target.Identity())
	if err != nil {
		return fmt.Errorf("cannot register %s: %w", target.Identity(), err)
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	task, err := client.CreateTask(ctx, protocol.SaveTaskRequest{
		Name:           fmt.Sprintf("%s %s %s", loaded.Name, target.Reference(), suffix),
		Prompt:         prompt,
		Runtime:        selected,
		TimeoutSeconds: int(loaded.Timeout.Seconds()),
		RepositoryIDs:  []string{repository.ID},
	})
	if err != nil {
		if factoryclient.Code(err) == "task_limit_reached" {
			return fmt.Errorf("%w\nEach dispatch currently records one task, and the control plane caps them. This is tracked as the schema collapse in the CLI issue", err)
		}
		return err
	}

	// The control plane scopes a request key to one task, and each dispatch
	// records its own task, so a caller-supplied key could never deduplicate a
	// repeated dispatch. Until a run carries the dispatch identity itself, the
	// key is generated here rather than promised to callers.
	detail, err := client.StartTask(ctx, task.ID, suffix+"-"+loaded.ShortHash())
	if err != nil {
		return err
	}

	return showDispatch(*format, loaded, target, detail)
}

// selectedPhases reads --phase and --phases.
//
// A multi-phase dispatch has to be one atomic call so that a partial fan-out is
// impossible, and it needs a dependency edge in the control plane to hold the
// second phase until the first succeeds. Neither exists yet, so a list of more
// than one is refused rather than silently started in parallel.
func selectedPhases(single, list string) ([]string, error) {
	single = strings.TrimSpace(single)
	list = strings.TrimSpace(list)
	switch {
	case single != "" && list != "":
		return nil, fmt.Errorf("use either --phase or --phases, not both")
	case single != "":
		return []string{single}, nil
	case list == "":
		return nil, fmt.Errorf("--phase is required; run `factory phases` to list the phases in this repository")
	}

	names := make([]string, 0, 3)
	for _, name := range strings.Split(list, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("--phases listed no phases")
	}
	if len(names) > 1 {
		return nil, fmt.Errorf("chaining %s needs a dependency edge the control plane does not have yet; dispatch one phase at a time with --phase", strings.Join(names, ","))
	}
	return names, nil
}

// resolveRuntime maps a phase runtime onto a runtime the control plane accepts.
// "claude" is accepted as the everyday spelling of claude-code.
func resolveRuntime(override, declared string) (string, error) {
	selected := strings.TrimSpace(override)
	if selected == "" {
		selected = strings.TrimSpace(declared)
	}
	if selected == "" {
		return protocol.RuntimeCodex, nil
	}
	if selected == "claude" {
		selected = protocol.RuntimeClaudeCode
	}
	if !protocol.SupportedRuntime(selected) {
		return "", fmt.Errorf("runtime %q is not supported; use one of %s", selected, strings.Join(protocol.SupportedRuntimes(), ", "))
	}
	return selected, nil
}

func randomSuffix() (string, error) {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("cannot generate a dispatch identifier: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func showDryRun(format string, loaded phase.Phase, work target, prompt string) error {
	if format == "json" {
		return writeJSON(map[string]any{
			"phase":      loaded.Name,
			"phase_hash": loaded.Hash,
			"work":       work.Reference(),
			"runtime":    loaded.Runtime,
			"timeout":    loaded.Timeout.String(),
			"read_only":  loaded.ReadOnly,
			"prompt":     prompt,
		})
	}
	fmt.Printf("phase    %s@%s\n", loaded.Name, loaded.ShortHash())
	fmt.Printf("work     %s\n", work.Reference())
	fmt.Printf("runtime  %s\n", orDefault(loaded.Runtime, "(default)"))
	fmt.Printf("timeout  %s\n", loaded.Timeout)
	fmt.Printf("\n%s\n", strings.TrimRight(prompt, "\n"))
	fmt.Fprintln(os.Stderr, "\nDry run: nothing was dispatched.")
	return nil
}

func showDispatch(format string, loaded phase.Phase, work target, detail protocol.RunDetail) error {
	if format == "json" {
		return writeJSON(map[string]any{
			"run":        detail.Run.ID,
			"state":      string(detail.Run.State),
			"phase":      loaded.Name,
			"phase_hash": loaded.Hash,
			"work":       work.Reference(),
			"sessions":   len(detail.Sessions),
		})
	}
	fmt.Printf("%s  %s  %s@%s  %s\n",
		detail.Run.ID, work.Reference(), loaded.Name, loaded.ShortHash(), detail.Run.State)
	fmt.Fprintf(os.Stderr, "\nFollow it with `factory logs %s --follow`.\n", detail.Run.ID)
	return nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func since(start time.Time) string {
	elapsed := time.Since(start).Truncate(time.Second)
	if elapsed < 0 {
		return "0s"
	}
	return elapsed.String()
}
