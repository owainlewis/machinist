package agentrunner

import (
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func TestNewKubernetesValidatesAndNeedsNoLocalCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	if _, err := NewKubernetes(config.Runner{}); err == nil || !strings.Contains(err.Error(), "kubernetes") {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewKubernetes(config.Runner{Kubernetes: &config.Kubernetes{}}); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	valid := config.Kubernetes{Context: "c", Namespace: "n", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64), Secret: "s", ProxyURL: "http://egress:3128"}
	if _, err := NewKubernetes(config.Runner{Kubernetes: &valid}); err != nil {
		t.Fatal(err)
	}
	valid.AllowedHosts = []string{"bad host,evil.test"}
	if _, err := NewKubernetes(config.Runner{Kubernetes: &valid}); err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
		t.Fatalf("error = %v", err)
	}
}
