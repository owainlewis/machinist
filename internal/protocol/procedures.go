package protocol

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Procedure is the product name for a saved Task. The alias keeps the current
// API and storage model compatible while operator surfaces adopt Procedure.
type Procedure = Task

type ProcedurePage struct {
	Procedures []Procedure `json:"procedures"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

type ProcedureRunRequest struct {
	RequestKey      string   `json:"request_key"`
	Procedure       string   `json:"procedure"`
	Repositories    []string `json:"repositories,omitempty"`
	AllRepositories bool     `json:"all_repositories"`
	Rebuild         bool     `json:"rebuild"`
}

type ProcedureRunAdmission struct {
	Result     AdmissionResult `json:"result"`
	RequestKey string          `json:"request_key"`
	Run        RunDetail       `json:"run"`
}

type NormalizedProcedureRunInput struct {
	Procedure       string
	Repositories    []string
	AllRepositories bool
	Rebuild         bool
}

func NormalizeProcedureRunRequest(input ProcedureRunRequest) (NormalizedProcedureRunInput, error) {
	procedure := strings.TrimSpace(input.Procedure)
	if procedure == "" || utf8.RuneCountInString(procedure) > 200 {
		return NormalizedProcedureRunInput{}, errors.New("procedure is required and limited to 200 characters")
	}
	value := NormalizedProcedureRunInput{
		Procedure: strings.ToLower(procedure), AllRepositories: input.AllRepositories,
		Rebuild: input.Rebuild,
	}
	if input.AllRepositories {
		if len(input.Repositories) != 0 {
			return NormalizedProcedureRunInput{}, errors.New("--repos all cannot be combined with explicit repositories")
		}
		return value, nil
	}
	if len(input.Repositories) < 1 || len(input.Repositories) > MaxWorkTargets {
		return NormalizedProcedureRunInput{}, fmt.Errorf("run requires between 1 and %d repositories", MaxWorkTargets)
	}
	seen := make(map[string]bool, len(input.Repositories))
	for _, raw := range input.Repositories {
		repository, err := NormalizeGitHubRepository(raw)
		if err != nil {
			return NormalizedProcedureRunInput{}, fmt.Errorf("invalid repository %q: %w", raw, err)
		}
		if seen[repository] {
			return NormalizedProcedureRunInput{}, fmt.Errorf("repository %s appears more than once", repository)
		}
		seen[repository] = true
		value.Repositories = append(value.Repositories, repository)
	}
	return value, nil
}

func ProcedureRunCallerFingerprint(input NormalizedProcedureRunInput) ([]byte, error) {
	encoded, err := json.Marshal(struct {
		Operation       string   `json:"operation"`
		Procedure       string   `json:"procedure"`
		Repositories    []string `json:"repositories"`
		AllRepositories bool     `json:"all_repositories"`
		Rebuild         bool     `json:"rebuild"`
	}{
		Operation: "run_procedure", Procedure: input.Procedure,
		Repositories: input.Repositories, AllRepositories: input.AllRepositories,
		Rebuild: input.Rebuild,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func CanonicalProcedureRunRequest(input ProcedureRunRequest) (
	ProcedureRunRequest,
	NormalizedProcedureRunInput,
	[]byte,
	error,
) {
	normalized, err := NormalizeProcedureRunRequest(input)
	if err != nil {
		return ProcedureRunRequest{}, NormalizedProcedureRunInput{}, nil, err
	}
	request := ProcedureRunRequest{
		RequestKey: strings.TrimSpace(input.RequestKey), Procedure: normalized.Procedure,
		Repositories:    append([]string(nil), normalized.Repositories...),
		AllRepositories: normalized.AllRepositories, Rebuild: normalized.Rebuild,
	}
	fingerprint, err := ProcedureRunCallerFingerprint(normalized)
	if err != nil {
		return ProcedureRunRequest{}, NormalizedProcedureRunInput{}, nil, err
	}
	return request, normalized, fingerprint, nil
}
