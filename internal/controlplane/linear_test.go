package controlplane

import (
	"io"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func TestLinearProjectsAndTeamsMustNameConnectedRepositories(t *testing.T) {
	linear := func(projects, teams map[string]string) config.Config {
		var loaded config.Config
		loaded.Server.DefaultPreset = "default"
		loaded.GitHub.Repositories = []string{"owner/machinist", "owner/airtime"}
		loaded.Linear = config.Linear{AppUserID: "app-user", AllowedUsers: []string{"owain"}, Projects: projects, Teams: teams}
		return loaded
	}
	poller, err := linearPoller(linear(map[string]string{"Airtime": "Owner/Airtime"}, map[string]string{"ENG": "owner/machinist"}), testFactory(), nil, nil, io.Discard)
	if err != nil || poller == nil {
		t.Fatalf("poller = %v, err = %v", poller, err)
	}
	if poller.Rules.Projects["Airtime"] != "Owner/Airtime" || poller.Rules.Teams["ENG"] != "owner/machinist" {
		t.Fatalf("rules = %+v", poller.Rules)
	}
	if poller, err := linearPoller(linear(map[string]string{"Airtime": "owner/airtime"}, nil), testFactory(), nil, nil, io.Discard); err != nil || poller == nil {
		t.Fatalf("projects alone: poller = %v, err = %v", poller, err)
	}
	_, err = linearPoller(linear(map[string]string{"Slip": "owner/slip"}, map[string]string{"ENG": "owner/machinist"}), testFactory(), nil, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "linear.projects.Slip: owner/slip is not in github.repositories") {
		t.Fatalf("error = %v", err)
	}
}
