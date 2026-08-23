package factorycli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestProcedureRunReusesImplicitKeyAndPreservesRepositoryOrder(t *testing.T) {
	dataHome := t.TempDir()
	requests := make(chan protocol.ProcedureRunRequest, 2)
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/procedure-runs" {
			http.NotFound(output, request)
			return
		}
		var input protocol.ProcedureRunRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		requests <- input
		if requestCount.Add(1) == 1 {
			connection, _, err := output.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		_ = json.NewEncoder(output).Encode(testProcedureRunAdmission(input.RequestKey, protocol.RunQueued))
	}))
	t.Cleanup(server.Close)
	getenv := func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	arguments := []string{
		"--server", server.URL, "run", "Bug-Fix", "--repos",
		"github.com/Acme/Web", "github.com/acme/API", "--rebuild",
	}
	var stderr bytes.Buffer
	if code := Run(Options{Arguments: arguments, Stderr: &stderr, Getenv: getenv}); code != 1 ||
		!strings.Contains(stderr.String(), "connect") {
		t.Fatalf("lost response = code %d, stderr %q", code, stderr.String())
	}
	first := <-requests
	if first.Procedure != "bug-fix" || !first.Rebuild || first.AllRepositories ||
		len(first.Repositories) != 2 || first.Repositories[0] != "github.com/acme/web" ||
		first.Repositories[1] != "github.com/acme/api" {
		t.Fatalf("first request = %#v", first)
	}
	journalPath := filepath.Join(dataHome, "operator", "admissions", "pending.json")
	journal := readJournalForTest(t, journalPath)
	if len(journal.Entries) != 1 || journal.Entries[0].RequestKey != first.RequestKey {
		t.Fatalf("pending journal = %#v", journal)
	}
	var stdout bytes.Buffer
	stderr.Reset()
	if code := Run(Options{Arguments: arguments, Stdout: &stdout, Stderr: &stderr, Getenv: getenv}); code != 0 {
		t.Fatalf("replay = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	second := <-requests
	if second.RequestKey == "" || second.RequestKey != first.RequestKey ||
		!strings.Contains(stdout.String(), "Procedure: Bug-Fix generation 3") ||
		!strings.Contains(stdout.String(), "github.com/acme/web") {
		t.Fatalf("second request = %#v, output %q", second, stdout.String())
	}
	if journal = readJournalForTest(t, journalPath); len(journal.Entries) != 0 {
		t.Fatalf("completed journal = %#v", journal)
	}
}

func TestProcedureRunAllAndTypedRejection(t *testing.T) {
	dataHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		var input protocol.ProcedureRunRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if !input.AllRepositories || len(input.Repositories) != 0 {
			t.Errorf("all request = %#v", input)
		}
		output.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(output).Encode(protocol.ErrorBody{Error: protocol.APIError{
			Code: "procedure_rebuild_active", Message: "matching Work is active",
			AdmissionResult: protocol.AdmissionRejectedBeforeCommit, RequestKey: input.RequestKey,
		}})
	}))
	t.Cleanup(server.Close)
	getenv := func(key string) string {
		if key == "FACTORY_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"--server", server.URL, "--json", "run", "bug-fix", "--repos", "all"},
		Stdout:    &stdout, Stderr: &stderr, Getenv: getenv,
	})
	if code != 1 || !strings.Contains(stdout.String(), `"result":"rejected_before_commit"`) {
		t.Fatalf("rejection = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	journal := readJournalForTest(t, filepath.Join(dataHome, "operator", "admissions", "pending.json"))
	if len(journal.Entries) != 0 {
		t.Fatalf("rejected journal = %#v", journal)
	}
}

func TestProceduresListsEveryPageWithRequiredFields(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/procedures" || request.URL.Query().Get("limit") != "200" {
			http.NotFound(output, request)
			return
		}
		requests++
		page := protocol.ProcedurePage{}
		if request.URL.Query().Get("cursor") == "" {
			page.Procedures = []protocol.Procedure{{
				Name: "Bug fix", Generation: 4, Runtime: protocol.RuntimeCodex,
				TimeoutSeconds: 7200, ConcurrencyLimit: 12,
			}}
			page.NextCursor = "next"
		} else {
			page.Procedures = []protocol.Procedure{{
				Name: "Archived review", Generation: 2, Runtime: protocol.RuntimePi,
				TimeoutSeconds: 300, ConcurrencyLimit: 1, Archived: true,
			}}
		}
		_ = json.NewEncoder(output).Encode(page)
	}))
	t.Cleanup(server.Close)
	var stdout, stderr bytes.Buffer
	if code := Run(Options{
		Arguments: []string{"--server", server.URL, "procedures"}, Stdout: &stdout, Stderr: &stderr,
	}); code != 0 || requests != 2 || !strings.Contains(stdout.String(), "GENERATION") ||
		!strings.Contains(stdout.String(), "Bug fix") || !strings.Contains(stdout.String(), "2h0m0s") ||
		!strings.Contains(stdout.String(), "Archived review") || !strings.Contains(stdout.String(), "true") {
		t.Fatalf("human Procedures = code %d, requests %d, stdout %q, stderr %q", code, requests, stdout.String(), stderr.String())
	}
	requests = 0
	stdout.Reset()
	stderr.Reset()
	if code := Run(Options{
		Arguments: []string{"--server", server.URL, "--json", "procedures"}, Stdout: &stdout, Stderr: &stderr,
	}); code != 0 {
		t.Fatalf("JSON Procedures = code %d, stderr %q", code, stderr.String())
	}
	var page protocol.ProcedurePage
	if err := json.Unmarshal(stdout.Bytes(), &page); err != nil || len(page.Procedures) != 2 || page.NextCursor != "" {
		t.Fatalf("JSON Procedures = %#v, err %v", page, err)
	}
}

func TestProcedureRunUsageValidation(t *testing.T) {
	input, _, _, err := parseProcedureRunArguments([]string{
		"-audit", "--repos", "github.com/acme/api",
	})
	if err != nil || input.Procedure != "-audit" {
		t.Fatalf("leading-hyphen Procedure = %#v, err %v", input, err)
	}
	for _, arguments := range [][]string{
		{"run"},
		{"run", "bug-fix"},
		{"run", "bug-fix", "--repos"},
		{"run", "bug-fix", "--repos", "all", "github.com/acme/api"},
		{"run", "bug-fix", "--repos", "github.com/acme/api", "--repos", "github.com/acme/web"},
	} {
		var stderr bytes.Buffer
		if code := Run(Options{Arguments: arguments, Stderr: &stderr}); code != 2 {
			t.Fatalf("Run(%#v) = %d, stderr %q", arguments, code, stderr.String())
		}
	}
}

func testProcedureRunAdmission(requestKey string, state protocol.RunState) protocol.ProcedureRunAdmission {
	run := protocol.Run{
		ID: "run-1", Task: protocol.TaskSnapshot{Name: "Bug-Fix", Generation: 3},
		State: state, SessionCount: 2, ActiveCount: 2,
	}
	return protocol.ProcedureRunAdmission{
		Result: protocol.AdmissionReplayed, RequestKey: requestKey,
		Run: protocol.RunDetail{Run: run, Sessions: []protocol.Session{
			{ID: "work-1", RepositoryIdentity: "github.com/acme/web", State: protocol.WorkQueued},
			{ID: "work-2", RepositoryIdentity: "github.com/acme/api", State: protocol.WorkQueued},
		}},
	}
}
