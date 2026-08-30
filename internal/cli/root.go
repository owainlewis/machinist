package cli

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	root.AddCommand(newValidateCommand(options))

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

func newValidateCommand(options *commandOptions) *cobra.Command {
	var workerConfigPath string
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the complete local Machinist configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			machinistConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			if err := controlplane.ValidateStore(machinistConfig.Server.Database); err != nil {
				return err
			}
			if err := controlplane.ValidateLoopbackListen(machinistConfig.Server.Listen); err != nil {
				return err
			}
			serverToken, err := machinistConfig.Server.WorkerToken()
			if err != nil {
				return err
			}
			schedules, err := config.LoadShepherdSchedules(machinistConfig.Path())
			if err != nil {
				return err
			}
			triggers, err := config.LoadTriggers(machinistConfig.Path())
			if err != nil {
				return err
			}
			if workerConfigPath == "" {
				workerConfigPath = filepath.Join(filepath.Dir(machinistConfig.Path()), "worker.toml")
			}
			workerConfig, err := config.LoadWorker(workerConfigPath)
			if err != nil {
				return err
			}
			if _, err := managedworker.New(workerConfig, io.Discard, io.Discard); err != nil {
				return err
			}
			if err := validateWorkerControlPlane(machinistConfig.Server.Listen, workerConfig.ControlPlane.URL); err != nil {
				return err
			}
			if err := validateConfiguredCommands(machinistConfig.Path(), workerConfig); err != nil {
				return err
			}
			if err := validateManagedWorkloads(schedules, triggers, workerConfig); err != nil {
				return err
			}
			workerToken, err := workerConfig.WorkerToken()
			if err != nil {
				return err
			}
			if subtle.ConstantTimeCompare([]byte(serverToken), []byte(workerToken)) != 1 {
				return errors.New("server and worker authentication tokens do not match")
			}
			_, err = fmt.Fprintln(options.stdout, "configuration is valid")
			return err
		},
	}
	validate.Flags().StringVar(&workerConfigPath, "worker-config", "", "managed worker configuration file")
	return validate
}

func validateConfiguredCommands(definitionPath string, worker config.Worker) error {
	definitions, err := config.LoadDefinitions(definitionPath)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(definitions.Commands))
	for name := range definitions.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		command, err := config.LoadCommand(definitionPath, name)
		if err != nil {
			return err
		}
		if _, err := worker.ResolveCommand(command); err != nil {
			return fmt.Errorf("validate command %q: %w", name, err)
		}
	}
	return nil
}

func validateManagedWorkloads(schedules []config.ResolvedShepherdSchedule, triggers []config.ResolvedTrigger, worker config.Worker) error {
	for _, schedule := range schedules {
		if _, ok := worker.Repositories[schedule.Repository]; !ok {
			return fmt.Errorf("shepherd schedule %q repository %q is not configured on this worker", schedule.Name, schedule.Repository)
		}
	}
	for _, trigger := range triggers {
		if _, err := worker.ResolveCommandModel(trigger.Command, trigger.Model); err != nil {
			return fmt.Errorf("trigger %q: %w", trigger.Identity, err)
		}
		if trigger.Family == "github" {
			for _, repository := range sortedKeys(trigger.GitHubRepositories) {
				if _, ok := worker.Repositories[repository]; !ok {
					return fmt.Errorf("trigger %q repository %q is not configured on this worker", trigger.Identity, repository)
				}
			}
			continue
		}
		if _, ok := worker.Repositories[trigger.Repository]; !ok {
			return fmt.Errorf("trigger %q repository %q is not configured on this worker", trigger.Identity, trigger.Repository)
		}
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// validateWorkerControlPlane verifies that the managed worker can reach the
// plain-HTTP loopback control plane configured alongside it. It performs no
// network operations.
func validateWorkerControlPlane(listen, rawURL string) error {
	listenHost, listenPort, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	listenPortNumber, err := strconv.Atoi(listenPort)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if listenPortNumber == 0 {
		return fmt.Errorf("managed worker control plane cannot use ephemeral listen port 0")
	}
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("invalid control_plane.url %q", rawURL)
	}
	if endpoint.Scheme != "http" {
		return fmt.Errorf("control_plane.url %q must use http for the configured local control plane", rawURL)
	}
	endpointPort := endpoint.Port()
	if endpointPort == "" {
		endpointPort = "80"
	}
	if endpointPort != listenPort || !sameLoopbackHost(listenHost, endpoint.Hostname()) {
		return fmt.Errorf("control_plane.url %q must target the configured local control plane at %s", rawURL, net.JoinHostPort(listenHost, listenPort))
	}
	return nil
}

func sameLoopbackHost(listenHost, endpointHost string) bool {
	if strings.EqualFold(listenHost, "localhost") || strings.EqualFold(endpointHost, "localhost") {
		return strings.EqualFold(listenHost, endpointHost)
	}
	listenIP := net.ParseIP(listenHost)
	endpointIP := net.ParseIP(endpointHost)
	return listenIP != nil && endpointIP != nil && listenIP.Equal(endpointIP)
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

type submitHTTPError struct {
	status int
	body   string
}

func (err *submitHTTPError) Error() string {
	if err.body == "" {
		return fmt.Sprintf("control plane returned %s", http.StatusText(err.status))
	}
	return fmt.Sprintf("control plane returned %s: %s", http.StatusText(err.status), err.body)
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
	token, err := worker.WorkerToken()
	if err != nil {
		return err
	}
	endpoint, err := controlPlaneEndpoint(worker.ControlPlane.URL)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	catalog, err := getSubmitCatalog(ctx, client, endpoint, token)
	if err != nil {
		return err
	}
	if !contains(catalog.Repositories, repository) {
		return fmt.Errorf("repository %q is not defined in the control plane; check the configured repository name and worker registration", repository)
	}
	if !contains(catalog.Commands, commandName) {
		return fmt.Errorf("command %q is not defined in the control plane", commandName)
	}

	body, err := json.Marshal(submitJobRequest{
		Prompt:     prompt,
		Repository: repository,
		Command:    commandName,
		Model:      model,
	})
	if err != nil {
		return fmt.Errorf("encode submission: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create submission request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return &submitHTTPError{status: response.StatusCode, body: strings.TrimSpace(string(message))}
	}
	var result submitJobResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode submission response: %w", err)
	}
	if strings.TrimSpace(result.ID) == "" {
		return errors.New("control plane submission response did not include a job ID")
	}
	fmt.Fprintln(options.stdout, result.ID)
	return nil
}

func getSubmitCatalog(ctx context.Context, client *http.Client, endpoint, token string) (submitCatalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/catalog", nil)
	if err != nil {
		return submitCatalog{}, fmt.Errorf("create control plane catalog request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return submitCatalog{}, fmt.Errorf("read control plane catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return submitCatalog{}, &submitHTTPError{status: response.StatusCode, body: strings.TrimSpace(string(message))}
	}
	var catalog submitCatalog
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		return submitCatalog{}, fmt.Errorf("decode control plane catalog: %w", err)
	}
	return catalog, nil
}

func controlPlaneEndpoint(raw string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", fmt.Errorf("invalid control_plane.url %q", raw)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", errors.New("control_plane.url must use http or https")
	}
	if endpoint.Scheme == "http" && !loopbackHost(endpoint.Hostname()) {
		return "", errors.New("control_plane.url must use https for a non-loopback host")
	}
	return strings.TrimRight(raw, "/"), nil
}

func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
