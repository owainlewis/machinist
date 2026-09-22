package protocol

// Revision records the exact version and feedback used by a revision attempt.
type Revision struct {
	PreviousRunID   string              `json:"previous_run_id"`
	PreviousSummary string              `json:"previous_summary"`
	Feedback        string              `json:"feedback"`
	PriorFeedback   []string            `json:"prior_feedback,omitempty"`
	Artifacts       map[string]Artifact `json:"artifacts,omitempty"`
}
