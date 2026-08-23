package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/owainlewis/factory/internal/phase"
)

func phasesCommand(arguments []string) error {
	set := flag.NewFlagSet("phases", flag.ContinueOnError)
	format := formatFlag(set)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if err := checkFormat(*format); err != nil {
		return err
	}

	loaded, err := phase.LoadAll(repositoryRoot())
	if err != nil {
		return err
	}

	if *format == "json" {
		listing := make([]map[string]any, 0, len(loaded))
		for _, item := range loaded {
			listing = append(listing, map[string]any{
				"name":        item.Name,
				"description": item.Description,
				"runtime":     item.Runtime,
				"model":       item.Model,
				"timeout":     item.Timeout.String(),
				"read_only":   item.ReadOnly,
				"hash":        item.Hash,
				"path":        item.Path,
			})
		}
		return writeJSON(listing)
	}

	if len(loaded) == 0 {
		fmt.Fprintf(os.Stderr, "No phases in %s.\n", phase.Dir)
		return nil
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "PHASE\tVERSION\tRUNTIME\tTIMEOUT\tDESCRIPTION")
	for _, item := range loaded {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			item.Name,
			item.ShortHash(),
			orDefault(item.Runtime, "-"),
			item.Timeout,
			truncate(item.Description, 56))
	}
	return writer.Flush()
}

func validateCommand(arguments []string) error {
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	loaded, err := phase.LoadAll(repositoryRoot())
	if err != nil {
		return err
	}
	if len(loaded) == 0 {
		return fmt.Errorf("no phase files in %s", phase.Dir)
	}
	for _, item := range loaded {
		fmt.Printf("ok  %s@%s  %s\n", item.Name, item.ShortHash(), item.Path)
	}
	fmt.Fprintf(os.Stderr, "\n%d phases are valid.\n", len(loaded))
	return nil
}
