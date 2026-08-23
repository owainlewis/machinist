package protocol

import (
	"bytes"
	"testing"
)

func TestCanonicalProcedureRunRequestPreservesOrderedSelectors(t *testing.T) {
	canonical, normalized, fingerprint, err := CanonicalProcedureRunRequest(ProcedureRunRequest{
		RequestKey: " key ", Procedure: " Bug-Fix ",
		Repositories: []string{"github.com/Acme/Web.git", "github.com/acme/API"},
		Rebuild:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if canonical.RequestKey != "key" || canonical.Procedure != "bug-fix" ||
		len(canonical.Repositories) != 2 || canonical.Repositories[0] != "github.com/acme/web" ||
		canonical.Repositories[1] != "github.com/acme/api" || !normalized.Rebuild {
		t.Fatalf("canonical request = %#v, normalized = %#v", canonical, normalized)
	}
	reordered, _, otherFingerprint, err := CanonicalProcedureRunRequest(ProcedureRunRequest{
		Procedure: "bug-fix", Repositories: []string{"github.com/acme/api", "github.com/acme/web"}, Rebuild: true,
	})
	if err != nil || reordered.Repositories[0] != "github.com/acme/api" || bytes.Equal(fingerprint, otherFingerprint) {
		t.Fatalf("reordered request = %#v, fingerprint changed = %v, err %v", reordered, !bytes.Equal(fingerprint, otherFingerprint), err)
	}
}

func TestNormalizeProcedureRunRequestValidatesRepositorySelection(t *testing.T) {
	for _, input := range []ProcedureRunRequest{
		{Procedure: "bug-fix"},
		{Procedure: "bug-fix", AllRepositories: true, Repositories: []string{"github.com/acme/api"}},
		{Procedure: "bug-fix", Repositories: []string{"github.com/acme/api", "github.com/ACME/API"}},
		{Procedure: " ", AllRepositories: true},
	} {
		if _, err := NormalizeProcedureRunRequest(input); err == nil {
			t.Fatalf("NormalizeProcedureRunRequest(%#v) succeeded", input)
		}
	}
	all, err := NormalizeProcedureRunRequest(ProcedureRunRequest{Procedure: "bug-fix", AllRepositories: true})
	if err != nil || !all.AllRepositories || len(all.Repositories) != 0 {
		t.Fatalf("all selection = %#v, err %v", all, err)
	}
}
