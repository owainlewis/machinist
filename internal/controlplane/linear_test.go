package controlplane

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

// linearIntake is an intake over a fresh store that connects the
// repositories in config.yaml.
func linearIntake(t *testing.T, repositories ...string) *sources.Intake {
	t.Helper()
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &sources.Intake{Store: store, Repositories: repositories}
}

func linearConfig(projects, teams map[string]string) config.Config {
	var loaded config.Config
	loaded.Server.DefaultPreset = "default"
	loaded.GitHub.Repositories = []string{"owner/machinist", "owner/airtime"}
	loaded.Linear = config.Linear{AppUserID: "app-user", AllowedUsers: []string{"owain"}, Projects: projects, Teams: teams}
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
