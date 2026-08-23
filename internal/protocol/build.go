package protocol

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	StandardBuildProcedureID         = "00000000-0000-4341-8000-000000000341"
	StandardBuildProcedureName       = "standard-build"
	StandardBuildProcedureGeneration = 1
	StandardBuildTimeoutSeconds      = 2 * 60 * 60
	StandardBuildConcurrencyLimit    = 10
)

const StandardBuildProcedurePrompt = `Complete the assigned software work item in this repository.

Follow every repository instruction. Read the live work item with the tools available on this Worker, refine the requested outcome, implement it completely, and verify it with the relevant tests, linters, builds, or application checks. Preserve unrelated changes.

Use a fresh review subagent that did not write the code. Fix every valid finding and repeat the relevant checks and review. Create or update one pull request, handle its CI and review findings, and push the final committed work to the immutable Factory publish branch supplied for this Work.

This Work is unfinished until you report an outcome with factory update. Allowed statuses are running, ready, needs-input, failed, and no-change. Use running updates only when they help the operator. Ready requires the pull-request URL and delivery evidence. Needs-input ends the current Attempt, so commit and push all partial work first and explain exactly what the operator must answer. Failed and no-change require a concise explanation.`

type BuildRequest struct {
	RequestKey          string   `json:"request_key"`
	References          []string `json:"references"`
	Repository          string   `json:"repository,omitempty"`
	RepositorySpecified bool     `json:"repository_specified"`
	Runtime             string   `json:"runtime,omitempty"`
	RuntimeSpecified    bool     `json:"runtime_specified"`
	Rebuild             bool     `json:"rebuild"`
}

type AdmissionResult string

const (
	AdmissionAdmitted             AdmissionResult = "admitted"
	AdmissionReplayed             AdmissionResult = "replayed"
	AdmissionRejectedBeforeCommit AdmissionResult = "rejected_before_commit"
)

type BuildAdmission struct {
	Result     AdmissionResult `json:"result"`
	RequestKey string          `json:"request_key"`
	Run        RunDetail       `json:"run"`
}

type NormalizedBuildReference struct {
	Reference          string
	SourceKind         string
	SourceKey          string
	RepositoryIdentity string
}

type NormalizedBuildInput struct {
	References          []NormalizedBuildReference
	Repository          string
	RepositorySpecified bool
	Runtime             string
	RuntimeSpecified    bool
	Rebuild             bool
}

func NormalizeBuildRequest(input BuildRequest) (NormalizedBuildInput, error) {
	if len(input.References) < 1 || len(input.References) > MaxWorkTargets {
		return NormalizedBuildInput{}, fmt.Errorf("build requires between 1 and %d references", MaxWorkTargets)
	}
	value := NormalizedBuildInput{
		RepositorySpecified: input.RepositorySpecified,
		RuntimeSpecified:    input.RuntimeSpecified,
		Rebuild:             input.Rebuild,
	}
	if input.RepositorySpecified {
		repository, err := NormalizeGitHubRepository(input.Repository)
		if err != nil {
			return NormalizedBuildInput{}, fmt.Errorf("invalid repository: %w", err)
		}
		value.Repository = repository
	} else if strings.TrimSpace(input.Repository) != "" {
		return NormalizedBuildInput{}, errors.New("repository_specified must be true when repository is provided")
	}
	if input.RuntimeSpecified {
		value.Runtime = strings.ToLower(strings.TrimSpace(input.Runtime))
		if value.Runtime == "" {
			return NormalizedBuildInput{}, errors.New("runtime must not be empty")
		}
	} else if strings.TrimSpace(input.Runtime) != "" {
		return NormalizedBuildInput{}, errors.New("runtime_specified must be true when runtime is provided")
	}
	value.References = make([]NormalizedBuildReference, 0, len(input.References))
	for _, raw := range input.References {
		reference, err := NormalizeBuildReference(raw)
		if err != nil {
			return NormalizedBuildInput{}, err
		}
		if reference.SourceKind == "opaque" {
			if !value.RepositorySpecified {
				return NormalizedBuildInput{}, fmt.Errorf("opaque reference %q requires --repo", reference.Reference)
			}
			reference.RepositoryIdentity = value.Repository
		} else if value.RepositorySpecified && reference.RepositoryIdentity != value.Repository {
			return NormalizedBuildInput{}, fmt.Errorf(
				"GitHub reference %q does not match --repo %s",
				reference.Reference,
				value.Repository,
			)
		}
		value.References = append(value.References, reference)
	}
	return value, nil
}

func NormalizeBuildReference(raw string) (NormalizedBuildReference, error) {
	reference := strings.TrimSpace(raw)
	if reference != raw {
		return NormalizedBuildReference{}, errors.New("build references must not contain leading or trailing whitespace")
	}
	if reference == "" {
		return NormalizedBuildReference{}, errors.New("build references must not be empty")
	}
	if len([]byte(reference)) > 2048 {
		return NormalizedBuildReference{}, errors.New("build reference exceeds 2048 bytes")
	}
	if strings.Contains(reference, "://") {
		return normalizeGitHubIssueURL(reference)
	}
	if len(reference) > 64 || !validOpaqueReference(reference) {
		return NormalizedBuildReference{}, fmt.Errorf(
			"invalid work-item reference %q: use an HTTPS GitHub issue URL or 1 to 64 ASCII letters, digits, dots, underscores, or hyphens",
			reference,
		)
	}
	return NormalizedBuildReference{
		Reference:  reference,
		SourceKind: "opaque",
		SourceKey:  reference,
	}, nil
}

func normalizeGitHubIssueURL(raw string) (NormalizedBuildReference, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Host, "github.com") {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "issues" {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	repository, err := url.PathUnescape(parts[1])
	if err != nil {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	identity, err := NormalizeGitHubRepository("github.com/" + owner + "/" + repository)
	if err != nil {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	number, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || number == 0 {
		return NormalizedBuildReference{}, fmt.Errorf("invalid GitHub issue URL %q", raw)
	}
	repositoryPath := strings.TrimPrefix(identity, "github.com/")
	canonical := "https://github.com/" + repositoryPath + "/issues/" + strconv.FormatUint(number, 10)
	return NormalizedBuildReference{
		Reference:          canonical,
		SourceKind:         "github_issue",
		SourceKey:          "github:" + repositoryPath + ":issue:" + strconv.FormatUint(number, 10),
		RepositoryIdentity: identity,
	}, nil
}

func NormalizeGitHubRepository(raw string) (string, error) {
	value := strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") || !validGitHubOwner(parts[1]) || !validGitHubRepository(parts[2]) {
		return "", errors.New("repository must use github.com/owner/repository")
	}
	return strings.ToLower(strings.Join(parts, "/")), nil
}

func BuildCallerFingerprint(input NormalizedBuildInput) ([]byte, error) {
	references := make([]string, 0, len(input.References))
	for _, reference := range input.References {
		references = append(references, reference.Reference)
	}
	encoded, err := json.Marshal(struct {
		Operation           string   `json:"operation"`
		References          []string `json:"references"`
		RepositorySpecified bool     `json:"repository_specified"`
		Repository          string   `json:"repository"`
		RuntimeSpecified    bool     `json:"runtime_specified"`
		Runtime             string   `json:"runtime"`
		Rebuild             bool     `json:"rebuild"`
	}{
		Operation: "build", References: references,
		RepositorySpecified: input.RepositorySpecified, Repository: input.Repository,
		RuntimeSpecified: input.RuntimeSpecified, Runtime: input.Runtime, Rebuild: input.Rebuild,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func CanonicalBuildRequest(input BuildRequest) (BuildRequest, NormalizedBuildInput, []byte, error) {
	normalized, err := NormalizeBuildRequest(input)
	if err != nil {
		return BuildRequest{}, NormalizedBuildInput{}, nil, err
	}
	request := BuildRequest{
		RequestKey:          strings.TrimSpace(input.RequestKey),
		Repository:          normalized.Repository,
		RepositorySpecified: normalized.RepositorySpecified,
		Runtime:             normalized.Runtime,
		RuntimeSpecified:    normalized.RuntimeSpecified,
		Rebuild:             normalized.Rebuild,
		References:          make([]string, 0, len(normalized.References)),
	}
	for _, reference := range normalized.References {
		request.References = append(request.References, reference.Reference)
	}
	fingerprint, err := BuildCallerFingerprint(normalized)
	if err != nil {
		return BuildRequest{}, NormalizedBuildInput{}, nil, err
	}
	return request, normalized, fingerprint, nil
}

func validOpaqueReference(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == '-')
		if !valid {
			return false
		}
	}
	return len(value) > 0
}

func validGitHubOwner(value string) bool {
	if len(value) < 1 || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validGitHubRepository(value string) bool {
	if len(value) < 1 || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}
