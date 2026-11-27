package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

// errReloadInvalid marks a reload failure caused by invalid configuration or
// factory files, which maps to HTTP 400.
var errReloadInvalid = errors.New("invalid reload")

// ReloadResult reports what a live reload applied and which differing settings
// require a control plane restart to take effect.
type ReloadResult struct {
	Changed        []string `json:"changed"`
	RestartNeeded  []string `json:"restart_needed,omitempty"`
	FactoryWarning string   `json:"factory_warning,omitempty"`
}

func invalidReload(err error) error {
	return fmt.Errorf("%w: %w", errReloadInvalid, err)
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request, tokenName string) {
	result, err := s.Reload(r.Context())
	if err != nil {
		if errors.Is(err, errReloadInvalid) {
			writeError(w, http.StatusBadRequest, errors.New(strings.TrimPrefix(err.Error(), errReloadInvalid.Error()+": ")))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	switch {
	case len(result.Changed) > 0:
		fmt.Fprintf(s.log, "machinist: %s reloaded configuration (%s)\n", tokenName, strings.Join(result.Changed, "; "))
	case len(result.RestartNeeded) > 0:
		fmt.Fprintf(s.log, "machinist: %s reloaded configuration (restart required for %s)\n", tokenName, strings.Join(result.RestartNeeded, ", "))
	default:
		fmt.Fprintf(s.log, "machinist: %s reloaded configuration (no changes)\n", tokenName)
	}
	writeJSON(w, http.StatusOK, result)
}

// Reload re-reads config.yaml and the factory directory and applies every
// reloadable change atomically. If anything in the new config or factory is
// invalid, nothing changes and an error is returned. Settings that need a
// restart are reported in ReloadResult.RestartNeeded and are not applied.
func (s *Server) Reload(ctx context.Context) (ReloadResult, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	prev := s.state()
	if prev.config.Path() == "" {
		return ReloadResult{}, invalidReload(errors.New("control plane was not started from a configuration file"))
	}

	nextConfig, err := config.LoadConfig(prev.config.Path())
	if err != nil {
		return ReloadResult{}, invalidReload(err)
	}
	if nextConfig.GitHub.Handle == "" {
		return ReloadResult{}, invalidReload(errors.New("github.handle is required"))
	}

	restartNeeded := restartSettingsDiff(prev.config, nextConfig)
	effectiveConfig := nextConfig
	effectiveConfig.Server.Listen = prev.config.Server.Listen
	effectiveConfig.Server.PublicURL = prev.config.Server.PublicURL
	effectiveConfig.Server.TrustedProxyHops = prev.config.Server.TrustedProxyHops
	effectiveConfig.Server.Database = prev.config.Server.Database
	effectiveConfig.Server.TLSCert = prev.config.Server.TLSCert
	effectiveConfig.Server.TLSKey = prev.config.Server.TLSKey
	effectiveConfig.Auth = prev.config.Auth
	effectiveConfig.Notify = prev.config.Notify

	nextFactory, err := factory.Load(effectiveConfig.Server.Factory)
	if err != nil {
		return ReloadResult{}, invalidReload(fmt.Errorf("load factory: %w", err))
	}
	if err := nextFactory.Validate(effectiveConfig.Server.DefaultPreset, effectiveConfig.GitHub.Repositories); err != nil {
		return ReloadResult{}, invalidReload(err)
	}

	nextApp, nextAppKeyHash, err := loadAppWithHash(effectiveConfig, nextFactory)
	if err != nil {
		return ReloadResult{}, invalidReload(err)
	}

	candidateIntake := &sources.Intake{Store: s.store, Repositories: effectiveConfig.GitHub.Repositories}
	nextLinear, err := linearPoller(ctx, effectiveConfig, nextFactory, candidateIntake, s.store, s.log)
	if err != nil {
		return ReloadResult{}, invalidReload(err)
	}
	if nextLinear != nil {
		nextLinear.Intake = s.intake
	}

	var queuedTickets []tickets.Ticket
	if s.store != nil {
		queuedTickets, err = s.store.QueuedTickets(ctx)
		if err != nil {
			return ReloadResult{}, fmt.Errorf("list queued jobs: %w", err)
		}
	}

	changed := diffReload(prev.config, prev.appKeyHash, prev.factory, effectiveConfig, nextAppKeyHash, nextFactory)
	linearChanged := linearPollerChanged(prev.config, prev.linear != nil, effectiveConfig, nextLinear != nil)

	var tokens engine.TokenMinter
	var identity engine.IdentityFinder
	if nextApp != nil {
		s.client.WithToken(nextApp.CachedToken)
		tokens = nextApp.Token
		identity = func(ctx context.Context) (string, string, error) {
			found, err := nextApp.Identity(ctx)
			return found.Name, found.Email, err
		}
	} else {
		s.client.WithToken(nil)
	}

	s.engine.Reload(
		queuedTickets,
		nextFactory,
		effectiveConfig.Server.RunDeadline(),
		effectiveConfig.GitHub.Handle,
		effectiveConfig.GitHub.AutoMerges(),
		effectiveConfig.GitHub.StewardRetryLimit(),
		tokens,
		identity,
	)
	s.intake.Update(
		effectiveConfig.Server.DefaultPreset,
		effectiveConfig.GitHub.Repositories,
		func(name string) bool {
			_, ok := nextFactory.Preset(name)
			return ok
		},
	)
	s.schedule.SetAutomations(nextFactory.Automations)

	nextPresets := presetViews(nextFactory)
	s.mu.Lock()
	s.config = effectiveConfig
	s.factory = nextFactory
	s.presets = nextPresets
	s.appKeyHash = nextAppKeyHash
	if nextApp != nil {
		s.app = nextApp
	} else {
		s.app = nil
	}
	if linearChanged {
		s.linear = nextLinear
	}
	s.githubCache.clear()
	s.mu.Unlock()

	if linearChanged {
		s.notifyLinear()
	}

	warning := factory.WarningLine(nextFactory.Updates)
	if warning != "" {
		fmt.Fprintf(s.log, "machinist: warning: %s\n", warning)
	}

	return ReloadResult{
		Changed:        changed,
		RestartNeeded:  restartNeeded,
		FactoryWarning: warning,
	}, nil
}

func loadAppWithHash(machinistConfig config.Config, loaded *factory.Factory) (*githubapp.App, [32]byte, error) {
	app, err := loadApp(machinistConfig, loaded)
	if err != nil || app == nil {
		return app, [32]byte{}, err
	}
	body, err := os.ReadFile(machinistConfig.GitHub.App.PrivateKeyFile)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("read the GitHub App private key: %w", err)
	}
	return app, sha256.Sum256(body), nil
}

func restartSettingsDiff(prev, next config.Config) []string {
	var needed []string
	if next.Server.Listen != prev.Server.Listen {
		needed = append(needed, "server.listen")
	}
	if next.Server.PublicURL != prev.Server.PublicURL {
		needed = append(needed, "server.public_url")
	}
	if next.Server.TrustedProxyHops != prev.Server.TrustedProxyHops {
		needed = append(needed, "server.trusted_proxy_hops")
	}
	if next.Server.Database != prev.Server.Database {
		needed = append(needed, "server.database")
	}
	if next.Server.TLSCert != prev.Server.TLSCert {
		needed = append(needed, "server.tls_cert")
	}
	if next.Server.TLSKey != prev.Server.TLSKey {
		needed = append(needed, "server.tls_key")
	}
	if authRestartDiff(prev.Auth, next.Auth) {
		needed = append(needed, "auth")
	}
	if next.Notify.Webhook != prev.Notify.Webhook {
		needed = append(needed, "notify.webhook")
	}
	return needed
}

func authRestartDiff(prev, next config.Auth) bool {
	if prev.TokenSigninEnabled() != next.TokenSigninEnabled() {
		return true
	}
	if prev.AllowTokenOnlyPublic != next.AllowTokenOnlyPublic {
		return true
	}
	if prev.BootstrapAdmin != next.BootstrapAdmin {
		return true
	}
	if prev.GitHub.Enabled != next.GitHub.Enabled {
		return true
	}
	if (prev.DisableTokenSignin == nil) != (next.DisableTokenSignin == nil) {
		return true
	}
	if prev.DisableTokenSignin != nil && *prev.DisableTokenSignin != *next.DisableTokenSignin {
		return true
	}
	return false
}

func diffReload(prevCfg config.Config, prevKeyHash [32]byte, prevFac *factory.Factory, nextCfg config.Config, nextKeyHash [32]byte, nextFac *factory.Factory) []string {
	changed := []string{}
	if nextCfg.Server.Factory != prevCfg.Server.Factory {
		changed = append(changed, "server.factory")
	}
	if nextCfg.Server.DefaultPreset != prevCfg.Server.DefaultPreset {
		changed = append(changed, fmt.Sprintf("server.default_preset: %s -> %s", prevCfg.Server.DefaultPreset, nextCfg.Server.DefaultPreset))
	}
	if nextCfg.Server.RunDeadline() != prevCfg.Server.RunDeadline() {
		changed = append(changed, fmt.Sprintf("server.run_timeout: %s -> %s", prevCfg.Server.RunDeadline(), nextCfg.Server.RunDeadline()))
	}
	if nextCfg.GitHub.Handle != prevCfg.GitHub.Handle {
		changed = append(changed, fmt.Sprintf("github.handle: %s -> %s", prevCfg.GitHub.Handle, nextCfg.GitHub.Handle))
	}
	if nextCfg.GitHub.AutoMerges() != prevCfg.GitHub.AutoMerges() {
		changed = append(changed, fmt.Sprintf("github.auto_merge: %s -> %s", strconv.FormatBool(prevCfg.GitHub.AutoMerges()), strconv.FormatBool(nextCfg.GitHub.AutoMerges())))
	}
	if nextCfg.GitHub.StewardRetryLimit() != prevCfg.GitHub.StewardRetryLimit() {
		changed = append(changed, fmt.Sprintf("github.steward_retries: %d -> %d", prevCfg.GitHub.StewardRetryLimit(), nextCfg.GitHub.StewardRetryLimit()))
	}
	if repoDiff := diffRepositories(prevCfg.GitHub.Repositories, nextCfg.GitHub.Repositories); repoDiff != "" {
		changed = append(changed, repoDiff)
	}
	if appDiff := diffGitHubApp(prevCfg.GitHub.App, prevKeyHash, nextCfg.GitHub.App, nextKeyHash); appDiff != "" {
		changed = append(changed, appDiff)
	}
	if linearDiff := diffLinear(prevCfg, nextCfg); linearDiff != "" {
		changed = append(changed, linearDiff)
	}
	changed = append(changed, diffFactory(prevFac, nextFac)...)
	return changed
}

func diffRepositories(prev, next []string) string {
	prevSet := make(map[string]bool, len(prev))
	for _, repo := range prev {
		prevSet[repo] = true
	}
	nextSet := make(map[string]bool, len(next))
	for _, repo := range next {
		nextSet[repo] = true
	}
	var added, removed []string
	for repo := range nextSet {
		if !prevSet[repo] {
			added = append(added, repo)
		}
	}
	for repo := range prevSet {
		if !nextSet[repo] {
			removed = append(removed, repo)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "github.repositories: " + strings.Join(parts, "; ")
}

func diffGitHubApp(prev *config.GitHubApp, prevHash [32]byte, next *config.GitHubApp, nextHash [32]byte) string {
	switch {
	case prev == nil && next == nil:
		return ""
	case prev == nil && next != nil:
		return "github.app: enabled"
	case prev != nil && next == nil:
		return "github.app: disabled"
	case prev.ID != next.ID || prev.PrivateKeyFile != next.PrivateKeyFile || prevHash != nextHash:
		return "github.app: updated"
	default:
		return ""
	}
}

func effectiveLinearPreset(cfg config.Config) string {
	if cfg.Linear.Preset != "" {
		return cfg.Linear.Preset
	}
	return cfg.Server.DefaultPreset
}

func linearPollerChanged(prevCfg config.Config, prevActive bool, nextCfg config.Config, nextActive bool) bool {
	if prevActive != nextActive {
		return true
	}
	if !nextActive {
		return false
	}
	prev, next := prevCfg.Linear, nextCfg.Linear
	return prev.AppUserID != next.AppUserID ||
		!slices.Equal(prev.AllowedUsers, next.AllowedUsers) ||
		effectiveLinearPreset(prevCfg) != effectiveLinearPreset(nextCfg) ||
		!maps.Equal(prev.Projects, next.Projects) ||
		!maps.Equal(prev.Teams, next.Teams) ||
		prev.MaxJobsPerHour != next.MaxJobsPerHour ||
		prev.Poll != next.Poll ||
		prev.ClientID != next.ClientID ||
		prev.ClientSecret != next.ClientSecret ||
		prev.Token != next.Token
}

func diffLinear(prevCfg, nextCfg config.Config) string {
	prevOn, nextOn := prevCfg.Linear.Enabled(), nextCfg.Linear.Enabled()
	switch {
	case !prevOn && !nextOn:
		return ""
	case !prevOn && nextOn:
		return "linear: enabled"
	case prevOn && !nextOn:
		return "linear: disabled"
	}
	prev, next := prevCfg.Linear, nextCfg.Linear
	var fields []string
	if prev.AppUserID != next.AppUserID {
		fields = append(fields, "app_user_id")
	}
	if !slices.Equal(prev.AllowedUsers, next.AllowedUsers) {
		fields = append(fields, "allowed_users")
	}
	if prev.Preset != next.Preset {
		fields = append(fields, "preset")
	}
	if !maps.Equal(prev.Projects, next.Projects) {
		fields = append(fields, "projects")
	}
	if !maps.Equal(prev.Teams, next.Teams) {
		fields = append(fields, "teams")
	}
	if prev.MaxJobsPerHour != next.MaxJobsPerHour {
		fields = append(fields, "max_jobs_per_hour")
	}
	if prev.Poll != next.Poll {
		fields = append(fields, "poll_interval")
	}
	if len(fields) == 0 {
		return ""
	}
	return "linear: updated (" + strings.Join(fields, ", ") + ")"
}

func diffFactory(prev, next *factory.Factory) []string {
	var changed []string
	if prev == nil {
		prev = &factory.Factory{}
	}
	if next == nil {
		next = &factory.Factory{}
	}

	for _, name := range sortedUnionKeys(prev.Presets, next.Presets) {
		oldPreset, hadOld := prev.Presets[name]
		newPreset, hasNew := next.Presets[name]
		switch {
		case !hadOld && hasNew:
			changed = append(changed, "preset added: "+name)
		case hadOld && !hasNew:
			changed = append(changed, "preset removed: "+name)
		case !presetsEqual(oldPreset, newPreset):
			changed = append(changed, "preset updated: "+name)
		}
	}

	for _, name := range sortedUnionKeys(prev.Agents, next.Agents) {
		oldAgent, hadOld := prev.Agents[name]
		newAgent, hasNew := next.Agents[name]
		fileKey := "agents/" + name + ".md"
		switch {
		case !hadOld && hasNew:
			changed = append(changed, "agent added: "+name)
		case hadOld && !hasNew:
			changed = append(changed, "agent removed: "+name)
		case oldAgent != newAgent || prev.Files[fileKey] != next.Files[fileKey]:
			changed = append(changed, "agent updated: "+name)
		}
	}

	for _, kind := range []struct{ prefix, label string }{
		{"prompts/", "prompt"},
		{"scripts/", "script"},
	} {
		prevFiles := filterPrefix(prev.Files, kind.prefix)
		nextFiles := filterPrefix(next.Files, kind.prefix)
		for _, name := range sortedUnionKeys(prevFiles, nextFiles) {
			oldBody, hadOld := prevFiles[name]
			newBody, hasNew := nextFiles[name]
			switch {
			case !hadOld && hasNew:
				changed = append(changed, kind.label+" added: "+name)
			case hadOld && !hasNew:
				changed = append(changed, kind.label+" removed: "+name)
			case oldBody != newBody:
				changed = append(changed, kind.label+" updated: "+name)
			}
		}
	}

	prevAutomations := indexAutomations(prev.Automations)
	nextAutomations := indexAutomations(next.Automations)
	for _, name := range sortedUnionKeys(prevAutomations, nextAutomations) {
		oldAuto, hadOld := prevAutomations[name]
		newAuto, hasNew := nextAutomations[name]
		switch {
		case !hadOld && hasNew:
			changed = append(changed, "automation added: "+name)
		case hadOld && !hasNew:
			changed = append(changed, "automation removed: "+name)
		case !automationsEqual(oldAuto, newAuto):
			changed = append(changed, "automation updated: "+name)
		}
	}

	return changed
}

func presetsEqual(a, b factory.Preset) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.Extends == b.Extends &&
		a.Timeout == b.Timeout &&
		a.Harness == b.Harness &&
		a.Model == b.Model &&
		slices.Equal(a.RunsOn, b.RunsOn) &&
		slices.Equal(a.Steps, b.Steps)
}

func automationsEqual(a, b factory.Automation) bool {
	if a.Name != b.Name || a.Repo != b.Repo || a.Preset != b.Preset || a.Harness != b.Harness || a.Model != b.Model || a.Prompt != b.Prompt || !slices.Equal(a.RunsOn, b.RunsOn) {
		return false
	}
	if (a.Cron == nil) != (b.Cron == nil) {
		return false
	}
	if a.Cron == nil {
		return true
	}
	return a.Cron.Expression() == b.Cron.Expression() && a.Cron.Timezone() == b.Cron.Timezone()
}

func indexAutomations(list []factory.Automation) map[string]factory.Automation {
	indexed := make(map[string]factory.Automation, len(list))
	for _, item := range list {
		indexed[item.Name] = item
	}
	return indexed
}

func filterPrefix(files map[string]string, prefix string) map[string]string {
	filtered := map[string]string{}
	for path, body := range files {
		if name, ok := strings.CutPrefix(path, prefix); ok {
			filtered[name] = body
		}
	}
	return filtered
}

func sortedUnionKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for key := range a {
		seen[key] = true
	}
	for key := range b {
		seen[key] = true
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
