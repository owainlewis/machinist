package factorycli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/buildinfo"
)

const defaultServer = "http://127.0.0.1:7337"

type Options struct {
	Arguments   []string
	Stdout      io.Writer
	Stderr      io.Writer
	Environment []string
	Getenv      func(string) string
	Executable  func() (string, error)
	LookPath    func(string) (string, error)
	Exec        func(string, []string, []string) error
	HTTPClient  *http.Client
}

type command struct {
	stdout      io.Writer
	stderr      io.Writer
	environment []string
	getenv      func(string) string
	executable  func() (string, error)
	lookPath    func(string) (string, error)
	exec        func(string, []string, []string) error
	client      *http.Client
}

type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

func Run(options Options) int {
	operation := newCommand(options)
	if err := operation.run(options.Arguments); err != nil {
		fmt.Fprintf(operation.stderr, "factory: %s\n", err)
		var usage *usageError
		if errors.As(err, &usage) {
			fmt.Fprintln(operation.stderr, "Run 'factory help' for usage.")
			return 2
		}
		return 1
	}
	return 0
}

func newCommand(options Options) command {
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	if options.Executable == nil {
		options.Executable = os.Executable
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.Exec == nil {
		options.Exec = replaceProcess
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return command{
		stdout: options.Stdout, stderr: options.Stderr,
		environment: options.Environment, getenv: options.Getenv,
		executable: options.Executable, lookPath: options.LookPath,
		exec: options.Exec, client: options.HTTPClient,
	}
}

func (c command) run(arguments []string) error {
	if buildinfo.Requested(arguments) {
		fmt.Fprintln(c.stdout, buildinfo.String("factory"))
		return nil
	}
	root := flag.NewFlagSet("factory", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	jsonOutput := root.Bool("json", false, "write one JSON value")
	server := root.String("server", c.serverEndpoint(), "loopback Factory server URL")
	if err := root.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.writeHelp()
			return nil
		}
		return &usageError{message: err.Error()}
	}
	remaining := root.Args()
	if len(remaining) == 0 {
		return &usageError{message: "a command is required"}
	}
	switch remaining[0] {
	case "help":
		if len(remaining) != 1 {
			return &usageError{message: "help does not accept arguments"}
		}
		c.writeHelp()
		return nil
	case "status":
		if len(remaining) != 1 {
			return &usageError{message: "status does not accept arguments"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client, err := newAPIClient(ctx, *server, c.client)
		if err != nil {
			return err
		}
		return c.status(ctx, client, *jsonOutput)
	case "show":
		if len(remaining) != 2 || strings.TrimSpace(remaining[1]) == "" {
			return &usageError{message: "show requires exactly one Run ID"}
		}
		if strings.ContainsAny(remaining[1], "/?#") {
			return &usageError{message: "Run ID must be one URL path segment"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client, err := newAPIClient(ctx, *server, c.client)
		if err != nil {
			return err
		}
		return c.show(ctx, client, remaining[1], *jsonOutput)
	case "workers":
		if len(remaining) != 1 {
			return &usageError{message: "workers does not accept arguments"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client, err := newAPIClient(ctx, *server, c.client)
		if err != nil {
			return err
		}
		return c.workers(ctx, client, *jsonOutput)
	case "server", "worker":
		if *jsonOutput {
			return &usageError{message: "--json is available only for status, show, and workers"}
		}
		return c.startProcess(remaining[0], remaining[1:])
	default:
		return &usageError{message: fmt.Sprintf("unknown command %q", remaining[0])}
	}
}

func (c command) serverEndpoint() string {
	if value := strings.TrimSpace(c.getenv("FACTORY_SERVER")); value != "" {
		return value
	}
	return defaultServer
}

func (c command) startProcess(role string, arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "start" {
		return &usageError{message: fmt.Sprintf("%s requires the start command", role)}
	}
	flags := flag.NewFlagSet("factory "+role+" start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "TOML configuration path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return &usageError{message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &usageError{message: fmt.Sprintf("unexpected %s start arguments: %s", role, strings.Join(flags.Args(), " "))}
	}
	configExplicit := false
	flags.Visit(func(value *flag.Flag) {
		configExplicit = configExplicit || value.Name == "config"
	})
	if configExplicit && strings.TrimSpace(*config) == "" {
		return &usageError{message: "--config requires a non-empty path"}
	}
	binary := "factory-" + role
	path, err := c.compatibilityBinary(binary)
	if err != nil {
		return err
	}
	environment := append([]string(nil), c.environment...)
	if configExplicit {
		key := "FACTORY_SERVER_CONFIG"
		if role == "worker" {
			key = "FACTORY_WORKER_CONFIG"
		}
		environment = setEnvironment(environment, key, *config)
	}
	if err := c.exec(path, []string{binary}, environment); err != nil {
		return fmt.Errorf("start %s: %w", role, err)
	}
	return nil
}

func (c command) compatibilityBinary(name string) (string, error) {
	executable, err := c.executable()
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		candidate := filepath.Join(filepath.Dir(executable), name)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	path, lookupErr := c.lookPath(name)
	if lookupErr == nil && isExecutable(path) {
		return path, nil
	}
	return "", fmt.Errorf("locate %s beside factory or on PATH", name)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func setEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func (c command) writeHelp() {
	fmt.Fprint(c.stdout, `Factory controls the local server and Workers and reads current operations.

Usage:
  factory [--server URL] [--json] status
  factory [--server URL] [--json] show RUN_ID
  factory [--server URL] [--json] workers
  factory server start [--config PATH]
  factory worker start [--config PATH]
  factory version

Options:
  --json        Write one JSON value to stdout.
  --server URL  Use this loopback HTTP endpoint (default http://127.0.0.1:7337).

Environment:
  FACTORY_SERVER  Overrides the endpoint used by finite commands.
`)
}
