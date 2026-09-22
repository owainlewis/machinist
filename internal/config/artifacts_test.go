package config

import (
	"github.com/owainlewis/machinist/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskTemplateDoesNotRetemplateUserInput(t *testing.T) {
	got, err := RenderTaskTemplate("{{task.spec}}\n{{inputs.plan}}\n{{stage.output_dir}}", protocol.Task{Spec: "literal {{inputs.plan}}"}, "/output", map[string]string{"plan": "/input"})
	if err != nil || got != "literal {{inputs.plan}}\n/input\n/output" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err = RenderTaskTemplate("{{inputs.missing}}", protocol.Task{}, "", nil); err == nil {
		t.Fatal("unknown input accepted")
	}
}
func TestArtifactStorageSettings(t *testing.T) {
	c := Config{Storage: Storage{Artifacts: ArtifactStorage{MaxFileSize: "4KB", MaxRunSize: "8KB"}}}
	r, err := c.ResolveStorage("/tmp/artifacts")
	if err != nil || r.MaxFileBytes != 4096 {
		t.Fatalf("%+v %v", r, err)
	}

}
func TestWorkflowArtifactReferences(t *testing.T) {
	for _, ref := range []string{"plan/spec.md", "build/spec.md", "missing/spec.md", "plan/../secret"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.toml")
		text := "[commands.plan]\nexecutor='test'\n[commands.build]\nexecutor='test'\n[workflows.deliver]\nsteps=[{command='plan',required_outputs=['spec.md']},{command='build',inputs={spec='" + ref + "'}}]\n"
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDefinitions(p)
		if err == nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
}

func TestArtifactStorageDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"saved-files", filepath.Join(dir, "absolute-files")} {
		configPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configPath, []byte("[storage.artifacts]\npath = '"+path+"'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadDefinitions(configPath)
		if err != nil {
			t.Fatal(err)
		}
		storage, err := c.ResolveStorage(filepath.Join(dir, "default"))
		expected := path
		if !filepath.IsAbs(path) {
			expected = filepath.Join(dir, path)
		}
		if err != nil || storage.Path != expected {
			t.Fatalf("%+v %v, want %s", storage, err, expected)
		}
	}
}
