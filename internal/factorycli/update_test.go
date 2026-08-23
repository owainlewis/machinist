package factorycli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestAgentUpdateUsesOnlyInjectedUnixSocketContext(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "factory-cli-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "update.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan protocol.AgentUpdateRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input protocol.AgentUpdateRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode update: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- input
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(protocol.WorkUpdate{
			ID: "update-1", WorkID: input.WorkID, AttemptID: input.AttemptID,
			RequestID: input.RequestID, Status: input.Status, Message: input.Message,
		})
	})}
	go server.Serve(listener)
	defer server.Close()

	environment := map[string]string{
		"FACTORY_WORK_ID":       "work-1",
		"FACTORY_ATTEMPT_ID":    "attempt-1",
		"FACTORY_UPDATE_SOCKET": socket,
		"FACTORY_UPDATE_TOKEN":  "agent-token",
	}
	var stdout, stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"update", "--status", "running", "--message", "Running tests."},
		Stdout:    &stdout, Stderr: &stderr,
		Getenv: func(key string) string { return environment[key] },
	})
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "accepted running") {
		t.Fatalf("Run update = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	select {
	case input := <-requests:
		if input.WorkID != "work-1" || input.AttemptID != "attempt-1" || input.UpdateToken != "agent-token" ||
			input.Status != protocol.WorkUpdateRunning || input.Message != "Running tests." || input.RequestID == "" {
			t.Fatalf("agent update request = %#v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("agent update request was not received")
	}
}

func TestAgentUpdateRequiresContextAndReadyEvidence(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run(Options{
		Arguments: []string{"update", "--status", "running", "--message", "Progress."},
		Stderr:    &stderr, Getenv: func(string) string { return "" },
	}); code != 1 || !strings.Contains(stderr.String(), "injected active agent-update") {
		t.Fatalf("missing context = %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := Run(Options{
		Arguments: []string{"update", "--status", "ready", "--message", "Ready."},
		Stderr:    &stderr,
		Getenv: func(key string) string {
			return map[string]string{
				"FACTORY_WORK_ID": "work", "FACTORY_ATTEMPT_ID": "attempt",
				"FACTORY_UPDATE_SOCKET": "/tmp/update", "FACTORY_UPDATE_TOKEN": "token",
			}[key]
		},
	}); code != 2 || !strings.Contains(stderr.String(), "ready requires --pr") {
		t.Fatalf("missing ready PR = %d, stderr %q", code, stderr.String())
	}
}
