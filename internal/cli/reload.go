package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/spf13/cobra"
)

const (
	controlPlaneStateFile = "control-plane.json"
	reloadTimeout         = 30 * time.Second
	maxReloadResponse     = 1 << 20
)

type controlPlaneState struct {
	URL      string `json:"url"`
	Database string `json:"database"`
}

func controlPlaneURL(listen string, tls bool) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		scheme := "http"
		if tls {
			scheme = "https"
		}
		return scheme + "://" + listen
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// writeControlPlaneState records the running control plane's bound address and
// database path so local commands such as machinist reload can still reach it
// even after server.listen or server.database is edited in config.yaml.
func writeControlPlaneState(machinistConfig config.Config, addr net.Addr) func() {
	dbPath := machinistConfig.Server.Database
	if config.IsPostgresURL(dbPath) {
		dbPath = ""
	}
	state := controlPlaneState{
		URL:      controlPlaneURL(addr.String(), machinistConfig.Server.TLSCert != ""),
		Database: dbPath,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return func() {}
	}
	encoded = append(encoded, '\n')

	var candidates []string
	if machinistConfig.Path() != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(machinistConfig.Path()), config.StateDir, controlPlaneStateFile))
	}
	if dbPath != "" {
		dbState := filepath.Join(filepath.Dir(dbPath), controlPlaneStateFile)
		if len(candidates) == 0 || dbState != candidates[0] {
			candidates = append(candidates, dbState)
		}
	}

	var written []string
	for _, path := range candidates {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			continue
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			continue
		}
		written = append(written, path)
	}
	return func() {
		for _, path := range written {
			if body, err := os.ReadFile(path); err == nil && bytes.Equal(body, encoded) {
				_ = os.Remove(path)
			}
		}
	}
}

func readControlPlaneStateFile(path string) (controlPlaneState, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return controlPlaneState{}, false
	}
	var state controlPlaneState
	if err := json.Unmarshal(body, &state); err != nil || state.URL == "" {
		return controlPlaneState{}, false
	}
	return state, true
}

func findControlPlaneState(configPath string) (controlPlaneState, bool) {
	path := configPath
	if path == "" {
		path = "config.yaml"
	}
	if abs, err := filepath.Abs(path); err == nil {
		if state, ok := readControlPlaneStateFile(filepath.Join(filepath.Dir(abs), config.StateDir, controlPlaneStateFile)); ok {
			return state, true
		}
	}
	if cfg, err := config.LoadConfig(configPath); err == nil && cfg.Server.Database != "" && !config.IsPostgresURL(cfg.Server.Database) {
		if state, ok := readControlPlaneStateFile(filepath.Join(filepath.Dir(cfg.Server.Database), controlPlaneStateFile)); ok {
			return state, true
		}
	}
	return controlPlaneState{}, false
}

func newReloadCommand(options *commandOptions) *cobra.Command {
	var serverURL string
	var token string
	command := &cobra.Command{
		Use:   "reload",
		Short: "Reload config.yaml and the factory on a running control plane",
		Long: "Ask a running Machinist control plane to re-read config.yaml and the factory directory without restarting. " +
			"By default it finds the control plane from the current configuration directory (or --config) and authenticates " +
			"using the local database. Pass --url and --token (or set MACHINIST_URL and MACHINIST_TOKEN) to reload a remote control plane.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReload(cmd.Context(), options, serverURL, token)
		},
	}
	command.Flags().StringVar(&serverURL, "url", "", "control plane URL (default $MACHINIST_URL or from config.yaml)")
	command.Flags().StringVar(&token, "token", "", "operator API token (default $MACHINIST_TOKEN or local database)")
	return command
}

func runReload(ctx context.Context, options *commandOptions, flagURL, flagToken string) error {
	targetURL := strings.TrimSpace(flagURL)
	if targetURL == "" {
		targetURL = strings.TrimSpace(os.Getenv("MACHINIST_URL"))
	}
	authToken := strings.TrimSpace(flagToken)
	if authToken == "" {
		authToken = strings.TrimSpace(os.Getenv("MACHINIST_TOKEN"))
	}

	var dbPath string
	if targetURL == "" || authToken == "" {
		if state, ok := findControlPlaneState(options.configPath); ok {
			if targetURL == "" {
				targetURL = state.URL
			}
			dbPath = state.Database
		}
		if targetURL == "" || (authToken == "" && dbPath == "") {
			cfg, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			if targetURL == "" {
				targetURL = controlPlaneURL(cfg.Server.Listen, cfg.Server.TLSCert != "")
			}
			if dbPath == "" {
				dbPath = cfg.Server.Database
			}
		}
	}

	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid control plane URL %q", targetURL)
	}
	targetURL = strings.TrimRight(targetURL, "/")

	if authToken == "" {
		store, err := tickets.Open(dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		ephemeralName, err := newReloadTokenName()
		if err != nil {
			return err
		}
		created, err := store.CreateToken(ctx, ephemeralName, tickets.RoleOperator)
		if err != nil {
			return fmt.Errorf("create reload token: %w", err)
		}
		defer func() {
			_ = store.DeleteToken(context.WithoutCancel(ctx), ephemeralName)
		}()
		authToken = created
	}

	result, err := postReload(ctx, targetURL, authToken)
	if err != nil {
		return err
	}
	printReloadResult(options.stdout, result)
	return nil
}

func newReloadTokenName() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "reload-" + hex.EncodeToString(buf), nil
}

func postReload(ctx context.Context, baseURL, token string) (controlplane.ReloadResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/reload", nil)
	if err != nil {
		return controlplane.ReloadResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: reloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return controlplane.ReloadResult{}, fmt.Errorf("could not reach control plane at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReloadResponse))
	if err != nil {
		return controlplane.ReloadResult{}, fmt.Errorf("read reload response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			return controlplane.ReloadResult{}, errors.New(failure.Error)
		}
		return controlplane.ReloadResult{}, fmt.Errorf("reload failed with HTTP %d", resp.StatusCode)
	}
	var result controlplane.ReloadResult
	if err := json.Unmarshal(body, &result); err != nil {
		return controlplane.ReloadResult{}, fmt.Errorf("decode reload response: %w", err)
	}
	return result, nil
}

func printReloadResult(out io.Writer, result controlplane.ReloadResult) {
	switch {
	case len(result.Changed) > 0:
		fmt.Fprintln(out, "Reloaded:")
		for _, item := range result.Changed {
			fmt.Fprintf(out, "  %s\n", item)
		}
	case len(result.RestartNeeded) > 0:
		fmt.Fprintln(out, "No reloadable changes applied.")
	default:
		fmt.Fprintln(out, "No changes.")
	}
	if len(result.RestartNeeded) > 0 {
		fmt.Fprintln(out, "Restart required to apply:")
		for _, setting := range result.RestartNeeded {
			fmt.Fprintf(out, "  %s\n", setting)
		}
	}
	if result.FactoryWarning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.FactoryWarning)
	}
}
