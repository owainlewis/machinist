package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/updater"
	"github.com/spf13/cobra"
)

type commandOptions struct {
	configPath          string
	machinistConfigPath string
	commandName         string
	prompt              string
	model               string
	repository          string
	stdin               io.Reader
	stdout              io.Writer
	stderr              io.Writer
	version             string
	listen              string
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	options := &commandOptions{stdin: stdin, stdout: stdout, stderr: stderr, version: version}
	root := newRootCommand(options)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var outcome *runner.OutcomeError
	if errors.As(err, &outcome) {
		fmt.Fprintf(stderr, "machinist: %s\n", outcome.Error())
		return outcome.ExitCode
	}
	var runtime *runner.RuntimeError
	if errors.As(err, &runtime) {
		fmt.Fprintf(stderr, "machinist: %s\n", runtime.Error())
		return 1
	}
	fmt.Fprintf(stderr, "machinist: %s\n", err)
	return 2
}

func newRootCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{
		Use:           "machinist",
		Short:         "Run approved commands as supervised workloads",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "configuration file")
	root.AddCommand(newInitCommand(options))
	root.AddCommand(newRunCommand(options))
	root.AddCommand(newSubmitCommand(options))
	root.AddCommand(newStartCommand(options))
	root.AddCommand(newUpdateCommand(options))

	worker := &cobra.Command{Use: "worker", Short: "Run or connect a Machinist Worker"}
	worker.AddCommand(newRunCommand(options))
	worker.AddCommand(newWorkerStartCommand(options))
	worker.AddCommand(newWorkerValidateCommand(options))
	root.AddCommand(worker)

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the Machinist version",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Fprintln(options.stdout, options.version)
		},
	})
	return root
}

func newWorkerValidateCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the managed worker configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			workerConfig, err := config.LoadWorker(options.configPath)
			if err != nil {
				return err
			}
			if _, err := managedworker.New(workerConfig, io.Discard, io.Discard); err != nil {
				return err
			}
			_, err = fmt.Fprintln(options.stdout, "worker configuration is valid")
			return err
		},
	}
}

func newUpdateCommand(options *commandOptions) *cobra.Command {
	var requestedVersion string
	update := &cobra.Command{
		Use:   "update",
		Short: "Update Machinist to a released version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := updater.Update(command.Context(), updater.Options{
				Version: requestedVersion,
				Current: options.version,
			})
			if err != nil {
				return err
			}
			if result.AlreadyCurrent {
				fmt.Fprintf(options.stdout, "machinist %s is already installed\n", result.Version)
				return nil
			}
			fmt.Fprintf(options.stdout, "updated machinist to %s\n", result.Version)
			return nil
		},
	}
	update.Flags().StringVar(&requestedVersion, "version", "", "release version to install, such as v0.2.0 (default latest)")
	return update
}

type submitCatalog struct {
	Commands     []string `json:"commands"`
	Repositories []string `json:"repositories"`
}

type submitJobRequest struct {
	Prompt     string `json:"prompt"`
	Repository string `json:"repository"`
	Command    string `json:"command"`
	Model      string `json:"model,omitempty"`
}

type submitJobResponse struct {
	ID string `json:"id"`
}

func newSubmitCommand(options *commandOptions) *cobra.Command {
	var commandName, prompt, model, repository string
	submit := &cobra.Command{
		Use:   "submit",
		Short: "Queue work for a managed Machinist Worker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return submitSelection(command.Context(), options, commandName, prompt, model, repository)
		},
	}
	submit.Flags().StringVar(&commandName, "command", "", "command name from the control plane")
	submit.Flags().StringVar(&prompt, "prompt", "", "work request supplied to the command on standard input (required)")
	submit.Flags().StringVar(&model, "model", "", "executor model or configured alias for this task")
	submit.Flags().StringVar(&repository, "repo", "", "configured repository name (required)")
	_ = submit.MarkFlagRequired("command")
	_ = submit.MarkFlagRequired("prompt")
	_ = submit.MarkFlagRequired("repo")
	return submit
}

func submitSelection(ctx context.Context, options *commandOptions, commandName, prompt, model, repository string) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	client, err := managedworker.NewClient(worker)
	if err != nil {
		return err
	}
	var catalog submitCatalog
	if err := client.Get(ctx, "/api/v1/catalog", &catalog); err != nil {
		return fmt.Errorf("read control plane catalog: %w", err)
	}
	if !slices.Contains(catalog.Repositories, repository) {
		return fmt.Errorf("repository %q is not defined in the control plane; check the configured repository name and worker registration", repository)
	}
	if !slices.Contains(catalog.Commands, commandName) {
		return fmt.Errorf("command %q is not defined in the control plane", commandName)
	}
	var result submitJobResponse
	request := submitJobRequest{Prompt: prompt, Repository: repository, Command: commandName, Model: model}
	if err := client.Post(ctx, "/api/v1/jobs", request, &result); err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	if strings.TrimSpace(result.ID) == "" {
		return errors.New("control plane submission response did not include a job ID")
	}
	fmt.Fprintln(options.stdout, result.ID)
	return nil
}

func newStartCommand(options *commandOptions) *cobra.Command {
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the Machinist control plane",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			machinistConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			serverConfig := machinistConfig.Server
			if options.listen != "" {
				serverConfig.Listen = options.listen
			}
			token, err := serverConfig.WorkerToken()
			if err != nil {
				return err
			}
			store, err := controlplane.OpenStore(serverConfig.Database)
			if err != nil {
				return err
			}
			defer store.Close()
			server, err := controlplane.NewServer(store, machinistConfig.Path(), token, serverConfig.ConcurrentJobLimit())
			if err != nil {
				return err
			}
			return server.Serve(command.Context(), serverConfig.Listen, func(address net.Addr) {
				fmt.Fprintf(options.stderr, "machinist: control plane listening on http://%s\n", address)
			})
		},
	}
	start.Flags().StringVar(&options.listen, "listen", "", "loopback listen address (default 127.0.0.1:7331)")
	return start
}

func newWorkerStartCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start a managed Machinist Worker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			workerConfig, err := config.LoadWorker(options.configPath)
			if err != nil {
				return err
			}
			worker, err := managedworker.New(workerConfig, options.stdout, options.stderr)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stderr, "machinist: worker %s connecting to %s\n", workerConfig.Name, workerConfig.ControlPlane.URL)
			return worker.Run(command.Context())
		},
	}
}

func newRunCommand(options *commandOptions) *cobra.Command {
	run := &cobra.Command{
		Use:   "run",
		Short: "Run one configured command in a Git repository",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runSelection(command.Context(), options)
		},
	}
	run.Flags().StringVar(&options.commandName, "command", "", "command name from the Machinist definition")
	_ = run.MarkFlagRequired("command")
	run.Flags().StringVar(&options.prompt, "prompt", "", "work request supplied to the command on standard input (required)")
	run.Flags().StringVar(&options.model, "model", "", "executor model or configured alias for this task")
	run.Flags().StringVar(&options.repository, "repo", ".", "Git repository path")
	run.Flags().StringVar(&options.machinistConfigPath, "machinist-config", "", "shared Machinist configuration file")
	_ = run.MarkFlagRequired("prompt")
	return run
}

func runSelection(ctx context.Context, options *commandOptions) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	definitionPath, err := worker.ResolveMachinistConfig(options.machinistConfigPath)
	if err != nil {
		return err
	}
	command, err := config.LoadCommand(definitionPath, options.commandName)
	if err != nil {
		return err
	}
	if err := validateDirectCommand(definitionPath, command); err != nil {
		return err
	}
	return runConfiguredCommand(ctx, options, worker, command)
}

func validateDirectCommand(definitionPath string, command config.ResolvedCommand) error {
	if command.Name == "shepherd" {
		schedules, err := config.LoadShepherdSchedules(definitionPath)
		if err != nil {
			return err
		}
		if len(schedules) > 0 {
			return errors.New("scheduled Shepherd cannot run directly; submit managed work so the control plane can enforce per-repository overlap protection")
		}
	}
	return nil
}

func runConfiguredCommand(ctx context.Context, options *commandOptions, worker config.Worker, configured config.ResolvedCommand) error {
	var err error
	configured, err = config.RenderPrompt(configured, options.prompt)
	if err != nil {
		return err
	}
	configured, err = worker.ResolveCommandModel(configured, options.model)
	if err != nil {
		return err
	}
	result, err := runner.Execute(ctx, runner.Options{
		Command:       configured,
		Repository:    options.repository,
		DataDirectory: worker.DataDirectory,
		Stdout:        options.stdout,
		Stderr:        options.stderr,
	})
	if result.ID != "" {
		fmt.Fprintf(options.stderr, "machinist: run %s %s; events: %s\n", result.ID, result.State, result.EventsPath)
	}
	return err
}
