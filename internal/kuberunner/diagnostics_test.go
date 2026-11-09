package kuberunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubectlFailureExplainsExpiredGoogleLoginWithoutLeakingOutput(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' 'private-output-canary: failed to retrieve access token: Reauthentication failed. cannot prompt during non-interactive execution. Please run gcloud auth login' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := Connector{Config: configuration()}
	err := c.ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "gcloud auth login") {
		t.Fatalf("missing authentication recovery: %v", err)
	}
	if strings.Contains(err.Error(), "install gVisor") || strings.Contains(err.Error(), "private-output-canary") {
		t.Fatalf("misleading or unsafe diagnostic: %v", err)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal("lost original command failure")
	}
}

func TestCommandDiagnosticsAreBoundedAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		missing            bool
	}{
		{"google", "Reauthentication failed. cannot prompt", "gcloud auth login", false},
		{"unauthorized", "error: You must be logged in to the server (Unauthorized)", "authentication failed", false},
		{"forbidden", "Error from server (Forbidden): private-user cannot get secrets", "RBAC", false},
		{"namespace", `Error from server (NotFound): namespaces "private-runners" not found`, "namespace does not exist", false},
		{"secret-named-executable", `Error from server (NotFound): secrets "executable" not found`, "does not exist", true},
		{"secret-named-context", `Error from server (NotFound): secrets "context" not found`, "does not exist", true},
		{"missing", "Error from server (NotFound): runtimeclasses.node.k8s.io gvisor not found", "does not exist", true},
		{"network", "Unable to connect to the server: dial tcp: no such host", "connectivity", false},
		{"plugin", "getting credentials: exec: executable gke-gcloud-auth-plugin not found", "plugin is missing", false},
		{"context", "error: context private-cluster does not exist", "kubeconfig", false},
		{"unknown", "private-output-canary", "check cluster access", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d commandDiagnostics
			// Split writes exercise diagnostic lines arriving across pipe reads.
			for _, b := range []byte(tc.output) {
				_, _ = d.Write([]byte{b})
			}
			_, _ = d.Write([]byte(strings.Repeat(" private-output-canary", 10000)))
			cause := errors.New("exit failure")
			err := d.failure(cause)
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-output-canary") {
				t.Fatalf("diagnostic: %v", err)
			}
			if len(d.tail) > 8192 {
				t.Fatal("unbounded stderr")
			}
			if resourceMissing(err) != tc.missing || !errors.Is(err, cause) {
				t.Fatalf("classification/wrapping: %v", err)
			}
		})
	}
}

func TestReadinessReportsMissingResourcesOnlyWhenNotFound(t *testing.T) {
	for _, kind := range []string{"runtimeclass", "networkpolicy", "secret", "deployment"} {
		for _, missing := range []bool{false, true} {
			t.Run(kind+fmt.Sprint(missing), func(t *testing.T) {
				c := Connector{Config: configuration()}
				c.run = func(_ context.Context, _ io.Reader, _ int, args ...string) ([]byte, error) {
					if args[1] == kind {
						return nil, &commandFailure{cause: errors.New("exit failure"), hint: "cluster authentication failed", missing: missing}
					}
					return []byte(`{"status":{"availableReplicas":1}}`), nil
				}
				err := c.ready(t.Context())
				if err == nil {
					t.Fatal("expected prerequisite failure")
				}
				recovery := strings.Contains(err.Error(), "configure gVisor") || strings.Contains(err.Error(), "run machinist kubernetes") || strings.Contains(err.Error(), "create it with GH_TOKEN")
				if recovery != missing {
					t.Fatalf("incorrect recovery: %v", err)
				}
			})
		}
	}
}

func TestCommandPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (Connector{Config: configuration()}).command(ctx, nil, 128, "get", "pods")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestMissingNamespaceIsNotReportedAsAMissingResource(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' 'Error from server (NotFound): namespaces \"runners\" not found' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := Connector{Config: configuration()}
	err := c.ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "namespace does not exist") {
		t.Fatalf("error = %v", err)
	}
}
