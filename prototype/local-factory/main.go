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
	response, err := client.Post(serverURL(cfg.Config.Server.Listen)+"/api/run", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("connect to factory web: %w", err)
	}
	defer response.Body.Close()
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
	response, err := client.Get(serverURL(cfg.Config.Server.Listen) + "/api/work")
	if err != nil {
		return fmt.Errorf("connect to factory web: %w", err)
	}
	defer response.Body.Close()
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
	executable, err := executablePath()
	if err != nil {
		return err
	}
	writes, _ := strconvParseBool(os.Getenv("FACTORY_GITHUB_WRITES"))
	runner := &agentRunner{config: cfg, store: newStore(cfg.Config.StateDirectory), github: ghClient{}, githubWrites: writes, executable: executable}
	ctx := context.Background()
	action := flags.Arg(0)
	switch action {
	case "delegate":
		if flags.NArg() != 2 {
			return errors.New("delegate requires a role")
		}
		output, err := runner.delegate(ctx, *workID, flags.Arg(1))
		if len(output) != 0 {
			_, _ = os.Stdout.Write(output)
		}
		return err
	case "publish-plan":
		return runner.publishPlan(ctx, *workID)
	case "finish":
		return runner.finish(ctx, *workID)
	case "block":
		if flags.NArg() < 2 {
			return errors.New("block requires a reason")
		}
		return runner.block(ctx, *workID, strings.Join(flags.Args()[1:], " "))
	default:
		return fmt.Errorf("unknown internal action %q", action)
	}
}

func takeFirstArgument(args []string) (string, []string, error) {
	for index, value := range args {
		if !strings.HasPrefix(value, "-") {
			return value, append(append([]string(nil), args[:index]...), args[index+1:]...), nil
		}
	}
	return "", args, errors.New("argument not found")
}

func strconvParseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true, nil
	case "", "0", "false", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", value)
	}
}
