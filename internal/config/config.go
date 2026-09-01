package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/triggers"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultTimeout           = 30 * time.Minute
	maxConfigBytes           = 1 << 20
	maxPromptBytes           = 256 << 10
	maxInputPromptBytes      = 256 << 10
	maxRenderedPromptBytes   = maxPromptBytes + maxInputPromptBytes
	maxTokenBytes            = 8 << 10
	defaultWorkerDirName     = ".machinist/worker"
	defaultServerDirName     = ".machinist/server"
	promptParameter          = "{{machinist.prompt}}"
	modelParameter           = "{{machinist.model}}"
	machinistParameterPrefix = "{{machinist"
	legacyFactoryPrefix      = "{{factory."
)

type Worker struct {
	Name          string                `toml:"name"`
	DataDirectory string                `toml:"data_directory"`
	ControlPlane  ControlPlane          `toml:"control_plane"`
	Executors     map[string]Executor   `toml:"executors"`
	Repositories  map[string]Repository `toml:"repositories"`
	configDir     string
}

type ControlPlane struct {
	URL       string `toml:"url"`
	TokenFile string `toml:"token_file"`
}

type Executor struct {
	Command []string          `toml:"command"`
	Models  map[string]string `toml:"models"`
}

func (e Executor) supportsModel() bool {
	return slices.ContainsFunc(e.Command, func(argument string) bool { return strings.Contains(argument, modelParameter) })
}

type Repository struct {
	Path string `toml:"path"`
}

type Server struct {
	Listen            string `toml:"listen"`
	Database          string `toml:"database"`
	WorkerTokenFile   string `toml:"worker_token_file"`
	MaxConcurrentJobs *int   `toml:"max_concurrent_jobs"`
	configDir         string
}

type Config struct {
	Server   Server                      `toml:"server"`
	Commands map[string]Command          `toml:"commands"`
	Shepherd map[string]ShepherdSchedule `toml:"shepherd"`
	GitHub   GitHub                      `toml:"github"`
	Triggers TriggerDefinitions          `toml:"triggers"`
	path     string
}

type GitHub struct {
	Repositories map[string]string `toml:"repositories"`
}

type TriggerDefinitions struct {
	GitHub   map[string]GitHubTrigger   `toml:"github"`
	Interval map[string]IntervalTrigger `toml:"interval"`
	Cron     map[string]CronTrigger     `toml:"cron"`
}

type TriggerSelection struct {
	Command string `toml:"command"`
	Model   string `toml:"model"`
}

type GitHubTrigger struct {
	TriggerSelection
	Every string `toml:"every"`
	Label string `toml:"label"`
}

type IntervalTrigger struct {
	TriggerSelection
	Every      string `toml:"every"`
	Repository string `toml:"repository"`
	Prompt     string `toml:"prompt"`
}

type CronTrigger struct {
	TriggerSelection
	Schedule   string `toml:"schedule"`
	Timezone   string `toml:"timezone"`
	Repository string `toml:"repository"`
	Prompt     string `toml:"prompt"`
}

type ResolvedTrigger struct {
	Identity           string
	Family             string
	Name               string
	Repository         string
	GitHubRepository   string
	GitHubRepositories map[string]string
	Every              time.Duration
	Schedule           string
	Timezone           string
	Label              string
	SelectionName      string
	Model              string
	Prompt             string
	Command            ResolvedCommand
	Signature          string
	cron               *triggers.Cron
}

type Command struct {
	Executor   string `toml:"executor"`
	PromptFile string `toml:"prompt_file"`
	Timeout    string `toml:"timeout"`
}

type ShepherdSchedule struct {
	Repository string `toml:"repository"`
	Every      string `toml:"every"`
	MaxActions int    `toml:"max_actions"`
}

type ResolvedShepherdSchedule struct {
	Name       string
	Repository string
	Every      time.Duration
	MaxActions int
	Prompt     string
	Command    ResolvedCommand
}

type ResolvedCommand struct {
	Name       string
	Executor   string
	Command    []string
	Model      string
	Prompt     string
	Timeout    time.Duration
	Definition string
	Hash       string
}

func LoadWorker(path string) (Worker, error) {
	worker := Worker{}
	if path == "" {
		defaultPath, err := defaultConfigPath("worker.toml")
		if err != nil {
			return Worker{}, err
		}
		path = defaultPath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			worker.configDir = filepath.Dir(path)
			return applyWorkerDefaults(worker)
		} else if err != nil {
			return Worker{}, fmt.Errorf("inspect worker config: %w", err)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Worker{}, fmt.Errorf("read worker config %q: %w", absPath, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&worker); err != nil {
		return Worker{}, fmt.Errorf("parse worker config %q: %w", absPath, err)
	}
	worker.configDir = filepath.Dir(absPath)
	return applyWorkerDefaults(worker)
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("config.toml")
		if err != nil {
			return Config{}, err
		}
		path = defaultPath
	}
	machinistConfig, err := loadConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Server.configDir = filepath.Dir(machinistConfig.path)
	machinistConfig.Server, err = applyServerDefaults(machinistConfig.Server)
	if err != nil {
		return Config{}, err
	}
	return machinistConfig, nil
}

func loadConfigFile(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve Machinist config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("read Machinist config %q: %w", absPath, err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(body, &raw); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	if _, ok := raw["pipelines"]; ok {
		return Config{}, fmt.Errorf("parse Machinist config %q: pipelines were removed; replace each pipeline with a repository-owned orchestration script configured under [commands]", absPath)
	}
	if _, ok := raw["agents"]; ok {
		return Config{}, fmt.Errorf("parse Machinist config %q: agents were renamed to commands; move [agents.NAME] definitions to [commands.NAME] and use --command", absPath)
	}
	if err := validateTriggerKeys(raw); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	machinistConfig := Config{path: absPath}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&machinistConfig); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	return machinistConfig, nil
}

func (c Config) Path() string { return c.path }

// CommandNames lists the defined command names in sorted order.
func (c Config) CommandNames() []string { return sortedMapKeys(c.Commands) }

func (w Worker) ResolveMachinistConfig(override string) (string, error) {
	if override != "" {
		return resolveConfigPath(override, "")
	}
	return resolveConfigPath("config.toml", w.configDir)
}

func (w Worker) ResolveCommandModel(command ResolvedCommand, requestedModel string) (ResolvedCommand, error) {
	executor, ok := w.Executors[command.Executor]
	if !ok {
		return ResolvedCommand{}, fmt.Errorf("executor %q is not configured on this worker", command.Executor)
	}
	if err := validateCommand(command.Executor, executor.Command); err != nil {
		return ResolvedCommand{}, err
	}
	model, err := resolveModel(command.Executor, executor, requestedModel)
	if err != nil {
		return ResolvedCommand{}, err
	}
	command.Command = make([]string, 0, len(executor.Command))
	for _, argument := range executor.Command {
		if model == "" && strings.Contains(argument, modelParameter) {
			continue
		}
		command.Command = append(command.Command, strings.ReplaceAll(argument, modelParameter, model))
	}
	command.Model = model
	return command, nil
}

func resolveModel(name string, executor Executor, requested string) (string, error) {
	model := strings.TrimSpace(requested)
	if model == "" {
		return "", nil
	}
	if !executor.supportsModel() {
		return "", fmt.Errorf("executor %q does not support model selection; add %s to its command", name, modelParameter)
	}
	if resolved, ok := executor.Models[model]; ok {
		model = strings.TrimSpace(resolved)
	} else if len(executor.Models) > 0 {
		return "", fmt.Errorf("model %q is not configured for executor %q", model, name)
	}
	if model == "" || len(model) > 128 || strings.ContainsAny(model, "\x00\r\n") {
		return "", fmt.Errorf("executor %q model is invalid", name)
	}
	return model, nil
}

func (w Worker) ResolveRepository(name string) (string, error) {
	repository, ok := w.Repositories[name]
	if !ok {
		return "", fmt.Errorf("repository %q is not configured on this worker", name)
	}
	return resolveConfigPath(repository.Path, w.configDir)
}

func (w Worker) WorkerToken() (string, error) {
	if strings.TrimSpace(w.ControlPlane.TokenFile) == "" {
		return "", errors.New("control_plane.token_file is required")
	}
	path, err := resolveConfigPath(w.ControlPlane.TokenFile, w.configDir)
	if err != nil {
		return "", err
	}
	return readToken(path)
}

func (w Worker) ExecutorNames() []string { return sortedMapKeys(w.Executors) }

func (w Worker) ModelCapabilities() map[string][]string {
	capabilities := make(map[string][]string)
	for name, executor := range w.Executors {
		if executor.supportsModel() {
			capabilities[name] = sortedMapKeys(executor.Models)
		}
	}
	return capabilities
}

func (w Worker) RepositoryNames() []string { return sortedMapKeys(w.Repositories) }

func (s Server) WorkerToken() (string, error) {
	return readToken(s.WorkerTokenFile)
}

func (s Server) ConcurrentJobLimit() int {
	if s.MaxConcurrentJobs == nil {
		return 0
	}
	return *s.MaxConcurrentJobs
}

func LoadCommand(definitionPath, name string) (ResolvedCommand, error) {
	if strings.TrimSpace(name) == "" {
		return ResolvedCommand{}, errors.New("command name is required")
	}
	definition, err := loadConfigFile(definitionPath)
	if err != nil {
		return ResolvedCommand{}, err
	}
	return definition.ResolveCommand(name)
}

// ResolveCommand resolves one named command from an already loaded definition.
func (c Config) ResolveCommand(name string) (ResolvedCommand, error) {
	command, ok := c.Commands[name]
	if !ok {
		return ResolvedCommand{}, fmt.Errorf("command %q is not defined in %s", name, c.path)
	}
	return resolveCommand(c.path, name, command)
}

func LoadDefinitions(path string) (Config, error) { return loadConfigFile(path) }

func LoadShepherdSchedules(path string) ([]ResolvedShepherdSchedule, error) {
	definition, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	if len(definition.Shepherd) == 0 {
		return nil, nil
	}
	command, ok := definition.Commands["shepherd"]
	if !ok {
		return nil, errors.New("shepherd schedules require a commands.shepherd definition")
	}
	resolvedCommand, err := resolveCommand(definition.path, "shepherd", command)
	if err != nil {
		return nil, err
	}
	seenRepositories := make(map[string]string)
	result := make([]ResolvedShepherdSchedule, 0, len(definition.Shepherd))
	for _, name := range sortedMapKeys(definition.Shepherd) {
		schedule := definition.Shepherd[name]
		repository := strings.TrimSpace(schedule.Repository)
		if strings.TrimSpace(name) == "" || repository == "" {
			return nil, errors.New("shepherd schedule names and repositories must be non-empty")
		}
		if previous, exists := seenRepositories[repository]; exists {
			return nil, fmt.Errorf("shepherd schedules %q and %q target the same repository %q", previous, name, repository)
		}
		seenRepositories[repository] = name
		every, err := time.ParseDuration(schedule.Every)
		if err != nil {
			return nil, fmt.Errorf("shepherd schedule %q every: %w", name, err)
		}
		if every < time.Minute {
			return nil, fmt.Errorf("shepherd schedule %q every must be at least 1m", name)
		}
		if schedule.MaxActions <= 0 {
			return nil, fmt.Errorf("shepherd schedule %q max_actions must be positive", name)
		}
		prompt := fmt.Sprintf("Run the scheduled Shepherd queue for repository %q with max_actions=%d. Perform at most %d mutating actions in this run.", repository, schedule.MaxActions, schedule.MaxActions)
		rendered, err := RenderPrompt(resolvedCommand, prompt)
		if err != nil {
			return nil, fmt.Errorf("render shepherd schedule %q: %w", name, err)
		}
		result = append(result, ResolvedShepherdSchedule{
			Name:       name,
			Repository: repository,
			Every:      every,
			MaxActions: schedule.MaxActions,
			Prompt:     prompt,
			Command:    rendered,
		})
	}
	return result, nil
}

func resolveCommand(definitionPath, name string, command Command) (ResolvedCommand, error) {
	if strings.TrimSpace(command.Executor) == "" {
		return ResolvedCommand{}, fmt.Errorf("command %q must define executor", name)
	}
	prompt := promptParameter
	if strings.TrimSpace(command.PromptFile) != "" {
		promptPath, err := expandHome(command.PromptFile)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("resolve command %q prompt: %w", name, err)
		}
		if !filepath.IsAbs(promptPath) {
			promptPath = filepath.Join(filepath.Dir(definitionPath), promptPath)
		}
		body, err := readBoundedFile(filepath.Clean(promptPath), maxPromptBytes)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("read command %q prompt %q: %w", name, promptPath, err)
		}
		prompt = string(body)
		if strings.TrimSpace(prompt) == "" {
			return ResolvedCommand{}, fmt.Errorf("command %q prompt is empty", name)
		}
		if err := validatePromptParameters(name, prompt); err != nil {
			return ResolvedCommand{}, err
		}
	}
	timeout := defaultTimeout
	if command.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(command.Timeout)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("command %q timeout: %w", name, err)
		}
		if timeout <= 0 {
			return ResolvedCommand{}, fmt.Errorf("command %q timeout must be positive", name)
		}
	}

	resolved := ResolvedCommand{
		Name:       name,
		Executor:   command.Executor,
		Prompt:     prompt,
		Timeout:    timeout,
		Definition: definitionPath,
	}
	var err error
	resolved.Hash, err = commandHash(resolved)
	if err != nil {
		return ResolvedCommand{}, err
	}
	return resolved, nil
}

func RenderPrompt(command ResolvedCommand, prompt string) (ResolvedCommand, error) {
	if strings.TrimSpace(prompt) == "" {
		return ResolvedCommand{}, errors.New("prompt is required")
	}
	if len(prompt) > maxInputPromptBytes {
		return ResolvedCommand{}, fmt.Errorf("prompt exceeds %d bytes", maxInputPromptBytes)
	}
	parameterCount := strings.Count(command.Prompt, promptParameter)
	if parameterCount == 0 {
		return ResolvedCommand{}, fmt.Errorf("command %q prompt must include %s", command.Name, promptParameter)
	}
	literalBytes := len(command.Prompt) - parameterCount*len(promptParameter)
	if literalBytes > maxRenderedPromptBytes || len(prompt) > (maxRenderedPromptBytes-literalBytes)/parameterCount {
		return ResolvedCommand{}, fmt.Errorf("rendered command prompt exceeds %d bytes", maxRenderedPromptBytes)
	}
	command.Prompt = strings.ReplaceAll(command.Prompt, promptParameter, prompt)
	return command, nil
}

func validatePromptParameters(commandName, prompt string) error {
	if strings.Contains(prompt, legacyFactoryPrefix) {
		return fmt.Errorf("command %q prompt uses the unsupported legacy Factory parameter namespace", commandName)
	}
	hasPrompt := false
	remaining := prompt
	for {
		start := strings.Index(remaining, machinistParameterPrefix)
		if start < 0 {
			break
		}
		remaining = remaining[start:]
		end := strings.Index(remaining, "}}")
		if end < 0 {
			return fmt.Errorf("command %q prompt contains a malformed Machinist parameter", commandName)
		}
		parameter := remaining[:end+2]
		if parameter != promptParameter {
			return fmt.Errorf("command %q prompt uses unsupported Machinist parameter %q", commandName, parameter)
		}
		hasPrompt = true
		remaining = remaining[end+2:]
	}
	if !hasPrompt {
		return fmt.Errorf("command %q prompt must include %s", commandName, promptParameter)
	}
	return nil
}

func applyWorkerDefaults(worker Worker) (Worker, error) {
	return applyWorkerDefaultsWithHostname(worker, os.Hostname)
}

func applyWorkerDefaultsWithHostname(worker Worker, getHostname func() (string, error)) (Worker, error) {
	worker.Name = strings.TrimSpace(worker.Name)
	if worker.Name == "" {
		hostname, err := getHostname()
		if err != nil {
			return Worker{}, fmt.Errorf("find machine hostname: %w", err)
		}
		worker.Name = strings.TrimSpace(hostname)
		if worker.Name == "" {
			return Worker{}, errors.New("find machine hostname: hostname is empty")
		}
	}
	if worker.DataDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Worker{}, fmt.Errorf("find user home directory: %w", err)
		}
		worker.DataDirectory = filepath.Join(home, filepath.FromSlash(defaultWorkerDirName))
	}
	dataDirectory, err := expandHome(worker.DataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	if !filepath.IsAbs(dataDirectory) && worker.configDir != "" {
		dataDirectory = filepath.Join(worker.configDir, dataDirectory)
	}
	worker.DataDirectory, err = filepath.Abs(dataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	worker.DataDirectory = filepath.Clean(worker.DataDirectory)
	for name, repository := range worker.Repositories {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(repository.Path) == "" {
			return Worker{}, errors.New("repository names and paths must be non-empty")
		}
		path, err := resolveConfigPath(repository.Path, worker.configDir)
		if err != nil {
			return Worker{}, fmt.Errorf("resolve repository %q: %w", name, err)
		}
		repository.Path = path
		worker.Repositories[name] = repository
	}
	for name, executor := range worker.Executors {
		if err := validateCommand(name, executor.Command); err != nil {
			return Worker{}, err
		}
		for alias, model := range executor.Models {
			if strings.TrimSpace(alias) == "" || strings.TrimSpace(model) == "" {
				return Worker{}, fmt.Errorf("executor %q model aliases and values must be non-empty", name)
			}
		}
		if len(executor.Models) > 0 && !executor.supportsModel() {
			return Worker{}, fmt.Errorf("executor %q defines models but its command does not contain %s", name, modelParameter)
		}
	}
	return worker, nil
}

func applyServerDefaults(server Server) (Server, error) {
	if server.Listen == "" {
		server.Listen = "127.0.0.1:7331"
	}
	if server.Database == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Server{}, fmt.Errorf("find user home directory: %w", err)
		}
		server.Database = filepath.Join(home, filepath.FromSlash(defaultServerDirName), "machinist.db")
	} else {
		path, err := resolveConfigPath(server.Database, server.configDir)
		if err != nil {
			return Server{}, fmt.Errorf("resolve database: %w", err)
		}
		server.Database = path
	}
	if strings.TrimSpace(server.WorkerTokenFile) == "" {
		return Server{}, errors.New("worker_token_file is required")
	}
	if server.MaxConcurrentJobs != nil && *server.MaxConcurrentJobs <= 0 {
		return Server{}, errors.New("max_concurrent_jobs must be positive")
	}
	tokenPath, err := resolveConfigPath(server.WorkerTokenFile, server.configDir)
	if err != nil {
		return Server{}, fmt.Errorf("resolve worker token file: %w", err)
	}
	server.WorkerTokenFile = tokenPath
	return server, nil
}

func validateCommand(name string, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("executor %q must define a non-empty command", name)
	}
	for index, argument := range command {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("executor %q command argument %d contains a null byte", name, index)
		}
		if strings.Contains(argument, legacyFactoryPrefix) {
			return fmt.Errorf("executor %q command uses the unsupported legacy Factory parameter namespace", name)
		}
		if index == 0 && strings.Contains(argument, modelParameter) {
			return fmt.Errorf("executor %q command executable cannot contain %s", name, modelParameter)
		}
		if strings.Contains(argument, modelParameter) && (!strings.HasPrefix(argument, "--") || !strings.HasSuffix(argument, "="+modelParameter) || strings.Count(argument, modelParameter) != 1) {
			return fmt.Errorf("executor %q must use %s as a complete optional --flag=%s argument", name, modelParameter, modelParameter)
		}
	}
	return nil
}

func resolveConfigPath(path, base string) (string, error) {
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func readToken(path string) (string, error) {
	body, err := readBoundedFile(path, maxTokenBytes)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func defaultConfigPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".machinist", name), nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))), nil
	}
	return path, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func commandHash(command ResolvedCommand) (string, error) {
	payload, err := json.Marshal(struct {
		Name     string        `json:"name"`
		Executor string        `json:"executor"`
		Prompt   string        `json:"prompt"`
		Timeout  time.Duration `json:"timeout"`
	}{command.Name, command.Executor, command.Prompt, command.Timeout})
	if err != nil {
		return "", fmt.Errorf("encode command definition: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
