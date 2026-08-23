package factorycli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestBuildReusesDurableImplicitKeyAfterLostResponseAcrossProcesses(t *testing.T) {
	dataHome := t.TempDir()
	requests := make(chan protocol.BuildRequest, 2)
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		var input protocol.BuildRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		requests <- input
		if requestCount.Add(1) == 1 {
			hijacker, ok := output.(http.Hijacker)
			if !ok {
				t.Error("response does not support hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		output.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(output).Encode(testBuildAdmission(input.RequestKey, protocol.RunQueued)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	getenv := func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	arguments := []string{"--server", server.URL, "build", "LINEAR-1", "--repo", "github.com/acme/api"}
	var firstError bytes.Buffer
	if code := Run(Options{Arguments: arguments, Stderr: &firstError, Getenv: getenv}); code != 1 ||
		!strings.Contains(firstError.String(), "connect") {
		t.Fatalf("lost response = code %d, stderr %q", code, firstError.String())
	}
	first := <-requests
	if len(first.References) != 1 || first.References[0] != "LINEAR-1" || first.Repository != "github.com/acme/api" {
		t.Fatalf("interspersed Build request = %#v", first)
	}
	journalPath := filepath.Join(dataHome, "operator", "admissions", "pending.json")
	journal := readJournalForTest(t, journalPath)
	if len(journal.Entries) != 1 || journal.Entries[0].RequestKey != first.RequestKey {
		t.Fatalf("pending journal = %#v, first request %#v", journal, first)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(Options{Arguments: arguments, Stdout: &stdout, Stderr: &stderr, Getenv: getenv}); code != 0 {
		t.Fatalf("replay = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	second := <-requests
	if second.RequestKey == "" || second.RequestKey != first.RequestKey {
		t.Fatalf("request keys = first %q, second %q", first.RequestKey, second.RequestKey)
	}
	if !strings.Contains(stdout.String(), "Request key: "+first.RequestKey) ||
		!strings.Contains(stdout.String(), "LINEAR-1") {
		t.Fatalf("replay output = %q", stdout.String())
	}
	journal = readJournalForTest(t, journalPath)
	if len(journal.Entries) != 0 {
		t.Fatalf("completed journal = %#v", journal)
	}
	assertFileMode(t, filepath.Dir(journalPath), 0o700)
	assertFileMode(t, journalPath, 0o600)
}

func TestBuildTypedRejectionClearsImplicitJournalAndWaitUsesDesignedExitCodes(t *testing.T) {
	dataHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		var input protocol.BuildRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		output.Header().Set("Content-Type", "application/json")
		if input.References[0] == "REJECT" {
			output.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(output).Encode(protocol.ErrorBody{Error: protocol.APIError{
				Code: "duplicate_build_active", Message: "matching Work is active",
				AdmissionResult: protocol.AdmissionRejectedBeforeCommit, RequestKey: input.RequestKey,
			}})
			return
		}
		state := protocol.RunSucceeded
		if input.References[0] == "QUESTION" {
			state = protocol.RunBlocked
		}
		_ = json.NewEncoder(output).Encode(testBuildAdmission(input.RequestKey, state))
	}))
	t.Cleanup(server.Close)
	getenv := func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"--server", server.URL, "--json", "build", "--repo", "github.com/acme/api", "REJECT"}
	if code := Run(Options{Arguments: arguments, Stdout: &stdout, Stderr: &stderr, Getenv: getenv}); code != 1 {
		t.Fatalf("rejection = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	var rejected struct {
		Result protocol.AdmissionResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rejected); err != nil || rejected.Result != protocol.AdmissionRejectedBeforeCommit {
		t.Fatalf("rejection JSON = %#v, error %v", rejected, err)
	}
	journal := readJournalForTest(t, filepath.Join(dataHome, "operator", "admissions", "pending.json"))
	if len(journal.Entries) != 0 {
		t.Fatalf("rejected journal = %#v", journal)
	}

	for _, test := range []struct {
		reference string
		wantCode  int
	}{{"DONE", 0}, {"QUESTION", 2}} {
		stdout.Reset()
		stderr.Reset()
		code := Run(Options{Arguments: []string{
			"--server", server.URL, "build", "--repo", "github.com/acme/api",
			"--request-key", "wait-" + strings.ToLower(test.reference), "--wait", test.reference,
		}, Stdout: &stdout, Stderr: &stderr, Getenv: getenv})
		if code != test.wantCode || !strings.Contains(stdout.String(), "Work: 1 total") || stderr.Len() != 0 {
			t.Fatalf("wait %s = code %d, stdout %q, stderr %q", test.reference, code, stdout.String(), stderr.String())
		}
	}
}

func TestBuildRetainsPendingKeyWhenAuthoritativeOutputFails(t *testing.T) {
	dataHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		var input protocol.BuildRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(output).Encode(testBuildAdmission(input.RequestKey, protocol.RunQueued))
	}))
	t.Cleanup(server.Close)
	getenv := func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	var stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"--server", server.URL, "build", "--repo", "github.com/acme/api", "PENDING"},
		Stdout:    failingWriter{}, Stderr: &stderr, Getenv: getenv,
	})
	if code != 1 || !strings.Contains(stderr.String(), "write Build output") {
		t.Fatalf("failed output = code %d, stderr %q", code, stderr.String())
	}
	journal := readJournalForTest(t, filepath.Join(dataHome, "operator", "admissions", "pending.json"))
	if len(journal.Entries) != 1 {
		t.Fatalf("failed-output journal = %#v", journal)
	}
}

func TestBuildScopeLockRejectsConcurrentDuplicateBeforeSending(t *testing.T) {
	dataHome := t.TempDir()
	command := newCommand(Options{Getenv: func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}})
	fingerprint := sha256.Sum256([]byte("same Build"))
	first, err := command.prepareImplicitAdmission("http://127.0.0.1:7337", fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()
	if _, err := command.prepareImplicitAdmission("http://127.0.0.1:7337", fingerprint[:]); err == nil ||
		!strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("concurrent lock error = %v", err)
	}
}

func TestBuildAdmissionScopeUsesStableNormalizedServerURL(t *testing.T) {
	client, err := newAPIClient(context.Background(), "http://LOCALHOST:07337/", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := client.admissionEndpoint, "http://localhost:7337"; got != want {
		t.Fatalf("admission endpoint = %q, want %q", got, want)
	}
	if client.endpoint.Hostname() == "localhost" {
		t.Fatalf("transport endpoint was not pinned: %q", client.endpoint)
	}
}

func TestBuildTerminatorKeepsOptionShapedReferencesLiteral(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	t.Cleanup(server.Close)
	var stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{
			"--server", server.URL, "build", "--", "--repo=github.com/acme/api", "LINEAR-1",
		},
		Stderr: &stderr,
	})
	if code != 2 || !strings.Contains(stderr.String(), "invalid work-item reference") || requests != 0 {
		t.Fatalf("terminated reference = code %d, stderr %q, requests %d", code, stderr.String(), requests)
	}
}

func testBuildAdmission(requestKey string, state protocol.RunState) protocol.BuildAdmission {
	run := protocol.Run{
		ID: "run-1", Task: protocol.TaskSnapshot{Name: protocol.StandardBuildProcedureName},
		State: state, SessionCount: 1,
	}
	workState := protocol.WorkQueued
	switch state {
	case protocol.RunSucceeded:
		run.ReadyCount = 1
		workState = protocol.WorkReady
	case protocol.RunBlocked:
		run.NeedsInputCount = 1
		workState = protocol.WorkNeedsInput
	default:
		run.ActiveCount = 1
	}
	return protocol.BuildAdmission{
		Result: protocol.AdmissionAdmitted, RequestKey: requestKey,
		Run: protocol.RunDetail{Run: run, Sessions: []protocol.Session{{
			ID: "work-1", RepositoryIdentity: "github.com/acme/api", State: workState,
			Target: protocol.WorkTarget{SourceReference: "LINEAR-1"},
		}}},
	}
}

func readJournalForTest(t *testing.T, path string) admissionJournal {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var journal admissionJournal
	if err := json.NewDecoder(bufio.NewReader(file)).Decode(&journal); err != nil {
		t.Fatal(err)
	}
	return journal
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
