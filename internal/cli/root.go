package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/spf13/cobra"
)

type commandOptions struct {
	configPath          string
	machinistConfigPath string
	agentName           string
	pipelineName        string
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
		Short:         "Run coding agents as supervised workloads",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "configuration file")
	root.AddCommand(newInitCommand(options))
	root.AddCommand(newRunCommand(options, true))
	root.AddCommand(newSubmitCommand(options))
	root.AddCommand(newStartCommand(options))

	worker := &cobra.Command{Use: "worker", Short: "Run or connect a Machinist Worker"}
	worker.AddCommand(newRunCommand(options, false))
	worker.AddCommand(newWorkerStartCommand(options))
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

type submitCatalog struct {
	Agents       []string `json:"agents"`
	Pipelines    []string `json:"pipelines"`
	Repositories []string `json:"repositories"`
}

type submitJobRequest struct {
	Prompt     string `json:"prompt"`
	Repository string `json:"repository"`
	Agent      string `json:"agent,omitempty"`
	Pipeline   string `json:"pipeline,omitempty"`
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
	var agentName, pipelineName, prompt, model, repository string
	submit := &cobra.Command{
		Use:   "submit",
		Short: "Queue work for a managed Machinist Worker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return submitSelection(command.Context(), options, agentName, pipelineName, prompt, model, repository)
		},
	}
	submit.Flags().StringVar(&agentName, "agent", "", "agent name from the control plane")
	submit.Flags().StringVar(&pipelineName, "pipeline", "", "pipeline name from the control plane")
	submit.Flags().StringVar(&prompt, "prompt", "", "work request supplied to the agent prompt (required)")
	submit.Flags().StringVar(&model, "model", "", "executor model or configured alias for this task")
	submit.Flags().StringVar(&repository, "repo", "", "configured repository name (required)")
	submit.MarkFlagsMutuallyExclusive("agent", "pipeline")
	submit.MarkFlagsOneRequired("agent", "pipeline")
	_ = submit.MarkFlagRequired("prompt")
	_ = submit.MarkFlagRequired("repo")
	return submit
}

func submitSelection(ctx context.Context, options *commandOptions, agentName, pipelineName, prompt, model, repository string) error {
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
	if agentName != "" && !contains(catalog.Agents, agentName) {
		return fmt.Errorf("agent %q is not defined in the control plane", agentName)
	}
	if pipelineName != "" && !contains(catalog.Pipelines, pipelineName) {
		return fmt.Errorf("pipeline %q is not defined in the control plane", pipelineName)
	}

	body, err := json.Marshal(submitJobRequest{
		Prompt:     prompt,
		Repository: repository,
		Agent:      agentName,
		Pipeline:   pipelineName,
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
			server, err := controlplane.NewServer(store, machinistConfig.Path(), token)
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

func newRunCommand(options *commandOptions, allowPipeline bool) *cobra.Command {
	short := "Run one configured agent in a Git repository"
	if allowPipeline {
		short = "Run a configured agent or pipeline in a Git repository"
	}
	run := &cobra.Command{
		Use:   "run",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runSelection(command.Context(), options)
		},
	}
	run.Flags().StringVar(&options.agentName, "agent", "", "agent name from the Machinist definition")
	if allowPipeline {
		run.Flags().StringVar(&options.pipelineName, "pipeline", "", "pipeline name from the Machinist definition")
		run.MarkFlagsMutuallyExclusive("agent", "pipeline")
		run.MarkFlagsOneRequired("agent", "pipeline")
	} else {
		_ = run.MarkFlagRequired("agent")
	}
	run.Flags().StringVar(&options.prompt, "prompt", "", "work request supplied to the agent prompt (required)")
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
	if options.pipelineName == "" {
		agent, err := config.LoadAgent(definitionPath, options.agentName)
		if err != nil {
			return err
		}
		return runAgent(ctx, options, worker, agent)
	}
	agents, err := config.LoadPipeline(definitionPath, options.pipelineName)
	if err != nil {
		return err
	}
	if err := config.ValidateModelSelection(agents, options.model); err != nil {
		return err
	}
	if options.model != "" {
		for _, agent := range agents {
			if _, err := worker.ResolveAgentModel(agent, options.model); err != nil {
				return err
			}
		}
	}
	for index, agent := range agents {
		fmt.Fprintf(options.stderr, "machinist: pipeline %s: agent %d/%d %s\n", options.pipelineName, index+1, len(agents), agent.Name)
		if err := runAgent(ctx, options, worker, agent); err != nil {
			return err
		}
	}
	return nil
}

func runAgent(ctx context.Context, options *commandOptions, worker config.Worker, agent config.ResolvedAgent) error {
	var err error
	agent, err = config.RenderPrompt(agent, options.prompt)
	if err != nil {
		return err
	}
	agent, err = worker.ResolveAgentModel(agent, options.model)
	if err != nil {
		return err
	}
	result, err := runner.Execute(ctx, runner.Options{
		Agent:         agent,
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
