package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, steps string
		valid       bool
	}{
		{"one", `["build"]`, true},
		{"sequence", `["triage", {command="build", approval="before"}]`, true},
		{"empty", `[]`, false},
		{"unknown", `["missing"]`, false},
		{"bad approval", `[{command="build",approval="after"}]`, false},
		{"typo", `[{command="build",approvel="before"}]`, false},
		{"invalid type", `[42]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			text := "[commands.triage]\nexecutor='codex'\n[commands.build]\nexecutor='claude'\n[workflows.deliver]\nsteps=" + test.steps + "\n"
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadDefinitions(path)
			if !test.valid {
				if err == nil {
					t.Fatal("expected invalid workflow")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			steps, err := c.ResolveTaskWorkflow("deliver", "")
			if err != nil {
				t.Fatal(err)
			}
			if !steps[0].SharedOutputs {
				t.Fatalf("prompt %q", steps[0].Command.Prompt)
			}
			if test.name == "sequence" && !steps[1].Approval {
				t.Fatal("missing approval")
			}
		})
	}
}

func TestRiskDeliveryExampleUsesSharedFolder(t *testing.T) {
	cfg, err := LoadDefinitions("../../examples/workflows/risk_delivery/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := cfg.ResolveTaskWorkflow("classify_then_merge", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("steps: %d", len(steps))
	}
	for _, step := range steps {
		if !step.SharedOutputs || len(step.Inputs) != 0 {
			t.Fatalf("not shared: %+v", step)
		}
	}
}
