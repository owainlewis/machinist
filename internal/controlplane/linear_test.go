package controlplane

import (
	"io"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/sources"
)

// linearIntake is an intake over a fresh store that connects the
// repositories in config.yaml.
func linearIntake(t *testing.T, repositories ...string) *sources.Intake {
	t.Helper()
	store := openTestStore(t)
	return &sources.Intake{Store: store, Repositories: repositories}
}

func linearConfig(projects, teams map[string]string) config.Config {
	var loaded config.Config
	loaded.Server.DefaultPreset = "default"
	loaded.GitHub.Repositories = []string{"owner/machinist", "owner/airtime"}
	loaded.Linear = config.Linear{AppUserID: "app-user", AllowedUsers: []string{"owain"}, Projects: projects, Teams: teams, Token: "lin_api_key"}
	return loaded
}

func TestLinearProjectsAndTeamsMustNameConnectedRepositories(t *testing.T) {
	intake := linearIntake(t, "owner/machinist", "owner/airtime")
	poller, err := linearPoller(t.Context(), linearConfig(map[string]string{"Airtime": "Owner/Airtime"}, map[string]string{"ENG": "owner/machinist"}), testFactory(), intake, intake.Store, io.Discard)
	if err != nil || poller == nil {
		t.Fatalf("poller = %v, err = %v", poller, err)
	}
	if poller.Rules.Projects["Airtime"] != "Owner/Airtime" || poller.Rules.Teams["ENG"] != "owner/machinist" {
		t.Fatalf("rules = %+v", poller.Rules)
	}
	if poller, err := linearPoller(t.Context(), linearConfig(map[string]string{"Airtime": "owner/airtime"}, nil), testFactory(), intake, intake.Store, io.Discard); err != nil || poller == nil {
		t.Fatalf("projects alone: poller = %v, err = %v", poller, err)
	}
	_, err = linearPoller(t.Context(), linearConfig(map[string]string{"Slip": "owner/slip"}, map[string]string{"ENG": "owner/machinist"}), testFactory(), intake, intake.Store, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "linear.projects.Slip: owner/slip is not a connected repository") {
		t.Fatalf("error = %v", err)
	}
}

// A repository added in Settings → Projects lives in the store, not in
// config.yaml, and Linear may route issues to it all the same.
func TestLinearMayNameARepositoryAddedInTheWebApp(t *testing.T) {
	intake := linearIntake(t, "owner/machinist", "owner/airtime")
	if err := intake.Store.AddProject(t.Context(), "owner/slip", "owain"); err != nil {
		t.Fatal(err)
	}
	poller, err := linearPoller(t.Context(), linearConfig(map[string]string{"Slip": "Owner/Slip"}, map[string]string{"ENG": "owner/slip"}), testFactory(), intake, intake.Store, io.Discard)
	if err != nil || poller == nil {
		t.Fatalf("poller = %v, err = %v", poller, err)
	}
	if poller.Rules.Projects["Slip"] != "Owner/Slip" || poller.Rules.Teams["ENG"] != "owner/slip" {
		t.Fatalf("rules = %+v", poller.Rules)
	}
}

func TestLinearRejectsPresetWithRequiredInputs(t *testing.T) {
	intake := linearIntake(t, "owner/machinist", "owner/airtime")
	emptyDefault := ""
	loaded := &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {
				Name:   "default",
				Inputs: map[string]factory.Input{"notes": {Default: &emptyDefault}},
				Steps:  []runner.Step{{Name: "work", Run: "true"}},
			},
			"deploy": {
				Name:   "deploy",
				Inputs: map[string]factory.Input{"environment": {Description: "Target env"}},
				Steps:  []runner.Step{{Name: "deploy", Run: "true"}},
			},
		},
	}

	// Preset with only optional inputs (including empty-string default) is allowed.
	if poller, err := linearPoller(t.Context(), linearConfig(nil, map[string]string{"ENG": "owner/machinist"}), loaded, intake, intake.Store, io.Discard); err != nil || poller == nil {
		t.Fatalf("optional-input preset rejected: poller = %v, err = %v", poller, err)
	}

	// Explicit linear.preset with required inputs is rejected.
	cfg := linearConfig(nil, map[string]string{"ENG": "owner/machinist"})
	cfg.Linear.Preset = "deploy"
	if _, err := linearPoller(t.Context(), cfg, loaded, intake, intake.Store, io.Discard); err == nil || !strings.Contains(err.Error(), "required inputs (environment)") {
		t.Fatalf("linear.preset with required input err = %v", err)
	}

	// Fallback to default_preset with required inputs is also rejected.
	cfg = linearConfig(nil, map[string]string{"ENG": "owner/machinist"})
	cfg.Server.DefaultPreset = "deploy"
	if _, err := linearPoller(t.Context(), cfg, loaded, intake, intake.Store, io.Discard); err == nil || !strings.Contains(err.Error(), "required inputs (environment)") {
		t.Fatalf("fallback default_preset with required input err = %v", err)
	}
}
