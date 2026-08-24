package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "init":
		return initCommand(args[1:])
	case "web":
		return webCommand(args[1:])
	case "run":
		return submitCommand(args[1:])
	case "status":
		return statusCommand(args[1:])
	case "internal":
		return internalCommand(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

const usage = `Usage:
  factory init DIRECTORY --repo OWNER/REPO --repo-path PATH [--base-ref REF]
  factory web [--config factory.toml] [--github-writes]
  factory run ISSUE [--config factory.toml]
  factory status [--config factory.toml]

factory web is the always-on process. It owns polling, scheduling, local
workers, durable state, and the monitoring UI. GitHub writes are off unless
--github-writes is explicitly supplied.
`

func usageError() error { return errors.New(strings.TrimSpace(usage)) }

func initCommand(args []string) error {
	directory, remaining, err := takeFirstArgument(args)
	if err != nil {
		return fmt.Errorf("init: directory is required")
	}
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repository := flags.String("repo", "", "GitHub owner/repository")
	repositoryPath := flags.String("repo-path", "", "local repository checkout")
	baseRef := flags.String("base-ref", "", "Git base ref; defaults to origin's default branch")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}
	if err := initialise(directory, *repository, *repositoryPath, *baseRef); err != nil {
		return err
	}
	fmt.Printf("Created %s\nNext: cd %s && factory web\n", directory, directory)
	return nil
}

func webCommand(args []string) error {
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "factory.toml", "config path")
	githubWrites := flags.Bool("github-writes", false, "allow issue, branch, and PR writes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	server, err := newServer(cfg, ghClient{}, *githubWrites, log.New(os.Stderr, "", log.LstdFlags))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.serve(ctx)
}

func submitCommand(args []string) error {
	reference, remaining, err := takeFirstArgument(args)
	if err != nil {
		return fmt.Errorf("run: GitHub issue is required")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "factory.toml", "config path")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"issue": reference})
	client := http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, serverURL(cfg.Config.Server.Listen)+"/api/run", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("connect to factory web: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 300 {
		return fmt.Errorf("factory web returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var item work
	if err := json.Unmarshal(responseBody, &item); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n%s\n", item.ID, item.State, serverURL(cfg.Config.Server.Listen))
	return nil
}

func statusCommand(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "factory.toml", "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, serverURL(cfg.Config.Server.Listen)+"/api/work", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("connect to factory web: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("factory web returned %s", response.Status)
	}
	_, err = io.Copy(os.Stdout, response.Body)
	return err
}

func internalCommand(args []string) error {
	flags := flag.NewFlagSet("internal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "config path")
	workID := flags.String("work", "", "work ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configPath == "" || *workID == "" || flags.NArg() == 0 {
		return errors.New("internal requires --config, --work, and an action")
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	authToken := os.Getenv("FACTORY_AUTH_TOKEN")
	if authToken == "" {
		return errors.New("internal command is not authorized by factory web")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	action := flags.Arg(0)
	actionArgs := flags.Args()[1:]
	switch action {
	case "delegate":
		if len(actionArgs) != 1 {
			return errors.New("delegate requires a role")
		}
	case "publish-plan":
		if len(actionArgs) != 0 {
			return errors.New("publish-plan does not accept arguments")
		}
	case "finish":
		if len(actionArgs) != 0 {
			return errors.New("finish does not accept arguments")
		}
	case "block":
		if len(actionArgs) == 0 {
			return errors.New("block requires a reason")
		}
	default:
		return fmt.Errorf("unknown internal action %q", action)
	}
	payload, _ := json.Marshal(struct {
		WorkID string   `json:"work_id"`
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}{WorkID: *workID, Action: action, Args: actionArgs})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL(cfg.Config.Server.Listen)+"/api/internal", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+authToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("connect to factory web: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return readErr
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("factory web returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	if len(responseBody) != 0 {
		_, _ = os.Stdout.Write(responseBody)
	}
	return nil
}

func takeFirstArgument(args []string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args, errors.New("argument not found")
	}
	return args[0], args[1:], nil
}
