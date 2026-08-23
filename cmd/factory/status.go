package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/factory/internal/factoryclient"
	"github.com/owainlewis/factory/internal/protocol"
)

func psCommand(ctx context.Context, arguments []string) error {
	set := flag.NewFlagSet("ps", flag.ContinueOnError)
	limit := set.Int("limit", 20, "maximum runs to list")
	all := set.Bool("all", false, "include runs that already finished")
	address := serverFlag(set)
	format := formatFlag(set)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if err := checkFormat(*format); err != nil {
		return err
	}

	client, err := factoryclient.New(*address)
	if err != nil {
		return err
	}
	runs, err := client.Runs(ctx, *limit)
	if err != nil {
		return err
	}
	if !*all {
		active := runs[:0]
		for _, item := range runs {
			if item.TerminalAt == nil {
				active = append(active, item)
			}
		}
		runs = active
	}

	if *format == "json" {
		return writeJSON(runs)
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stderr, "No runs. Dispatch one with `factory run <issue> --repo <owner/name> --phase build`.")
		return nil
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN\tTASK\tSTATE\tSESSIONS\tAGE")
	for _, item := range runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d/%d\t%s\n",
			item.ID,
			truncate(item.Task.Name, 44),
			item.State,
			item.SucceededCount,
			item.SessionCount,
			since(item.AdmittedAt))
	}
	return writer.Flush()
}

func logsCommand(ctx context.Context, arguments []string) error {
	runID, arguments := leadingArgument(arguments)
	set := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := set.Bool("follow", false, "keep polling until the run finishes")
	interval := set.Duration("interval", 2*time.Second, "poll interval when following")
	address := serverFlag(set)
	format := formatFlag(set)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if err := checkFormat(*format); err != nil {
		return err
	}
	if runID == "" {
		runID = set.Arg(0)
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("a run id is required; list them with `factory ps`")
	}

	client, err := factoryclient.New(*address)
	if err != nil {
		return err
	}

	var after int64
	for {
		detail, err := client.Run(ctx, runID)
		if err != nil {
			return err
		}
		attemptID, ok := latestAttempt(detail)
		if !ok {
			if !*follow {
				fmt.Fprintln(os.Stderr, "The run has not started on a worker yet.")
				return nil
			}
		} else {
			page, err := client.Events(ctx, attemptID, after)
			if err != nil {
				return err
			}
			for _, event := range page.Events {
				if err := printEvent(*format, event); err != nil {
					return err
				}
			}
			if page.NextAfter > after {
				after = page.NextAfter
			}
		}

		if !*follow || detail.Run.TerminalAt != nil {
			if detail.Run.TerminalAt != nil && *format == "table" {
				fmt.Fprintf(os.Stderr, "\nRun %s finished: %s\n", detail.Run.ID, detail.Run.State)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func cancelCommand(ctx context.Context, arguments []string) error {
	runID, arguments := leadingArgument(arguments)
	set := flag.NewFlagSet("cancel", flag.ContinueOnError)
	address := serverFlag(set)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if runID == "" {
		runID = set.Arg(0)
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("a run id is required; list them with `factory ps`")
	}
	client, err := factoryclient.New(*address)
	if err != nil {
		return err
	}
	if err := client.CancelRun(ctx, runID); err != nil {
		return err
	}
	fmt.Printf("%s cancelling\n", runID)
	return nil
}

// latestAttempt returns the newest attempt of a run. Events are recorded per
// attempt, so following a run means following its current attempt.
func latestAttempt(detail protocol.RunDetail) (string, bool) {
	var (
		newest protocol.Attempt
		found  bool
	)
	for _, session := range detail.Sessions {
		for _, attempt := range session.Attempts {
			if !found || attempt.CreatedAt.After(newest.CreatedAt) {
				newest, found = attempt, true
			}
		}
	}
	return newest.ID, found
}

func printEvent(format string, event protocol.AttemptEvent) error {
	if format == "json" {
		return writeJSON(event)
	}
	stamp := event.ServerTime.Format("15:04:05")
	fmt.Printf("%s  %-18s %s\n", stamp, event.Kind, summarise(event.Payload))
	return nil
}

// summarise renders an event payload as one line. Events carry arbitrary JSON,
// and a multi-line dump per event makes a follow unreadable.
func summarise(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(payload, &text) == nil {
		return truncate(collapse(text), 120)
	}
	var fields map[string]any
	if json.Unmarshal(payload, &fields) == nil {
		for _, key := range []string{"message", "text", "summary", "error"} {
			if value, ok := fields[key].(string); ok && value != "" {
				return truncate(collapse(value), 120)
			}
		}
	}
	return truncate(collapse(string(payload)), 120)
}

func collapse(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
