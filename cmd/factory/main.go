// Command factory is the operator command line for Factory.
//
// A phase is a versioned prompt file. A dispatch names the phase and the work.
// Phases never chain themselves: the sequence is chosen here, at dispatch, so
// that a phase file stays one prompt and Factory never grows a pipeline router.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/owainlewis/factory/internal/buildinfo"
)

const usage = `factory runs repeatable software work through coding agents.

Usage:
  factory run <work> [flags]     dispatch a phase against an issue or repository
  factory ps [flags]             list runs
  factory logs <run> [flags]     read the events of a run
  factory cancel <run>           stop a run
  factory phases [flags]         list the phases in this repository
  factory validate               check every phase file
  factory version                print the build version

Work may be an issue number with --repo, a GitHub issue URL, owner/repo#number,
or nothing when --repo names the target of a repository phase such as audit.

Common flags:
  --server <address>   control plane address (default 127.0.0.1:7337,
                       or $FACTORY_SERVER)
  -o <format>          output format: table (default) or json

Run factory <command> --help for the flags of one command.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "factory: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("a command is required")
	}

	if buildinfo.Requested(arguments) {
		fmt.Println(buildinfo.String("factory"))
		return nil
	}

	command, rest := arguments[0], arguments[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "run":
		return runCommand(ctx, rest)
	case "ps":
		return psCommand(ctx, rest)
	case "logs":
		return logsCommand(ctx, rest)
	case "cancel":
		return cancelCommand(ctx, rest)
	case "phases":
		return phasesCommand(rest)
	case "validate":
		return validateCommand(rest)
	case "version":
		fmt.Println(buildinfo.String("factory"))
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// serverFlag registers the shared control-plane address flag.
func serverFlag(set *flag.FlagSet) *string {
	address := os.Getenv("FACTORY_SERVER")
	return set.String("server", address, "control plane address (default 127.0.0.1:7337)")
}

// formatFlag registers the shared output format flag.
//
// The flag is -o rather than --json because gh, which is the tool most callers
// have seen, uses --json as a field selector. A caller passing a field list to
// a boolean flag would be a confusing first failure.
func formatFlag(set *flag.FlagSet) *string {
	return set.String("o", "table", "output format: table or json")
}

func checkFormat(format string) error {
	switch format {
	case "table", "json":
		return nil
	default:
		return fmt.Errorf("unknown output format %q: use table or json", format)
	}
}

// leadingArgument splits a leading positional argument from the flags.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `factory run 412 --repo acme/api` would otherwise ignore every flag. Callers
// write the subject first, so the subject is lifted out before parsing.
func leadingArgument(arguments []string) (string, []string) {
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		return arguments[0], arguments[1:]
	}
	return "", arguments
}

// repositoryRoot returns the directory holding .factory, which is the working
// directory unless FACTORY_ROOT overrides it.
func repositoryRoot() string {
	if root := strings.TrimSpace(os.Getenv("FACTORY_ROOT")); root != "" {
		return root
	}
	root, err := os.Getwd()
	if err != nil {
		return "."
	}
	return root
}
