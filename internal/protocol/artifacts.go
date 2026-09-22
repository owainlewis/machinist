package protocol

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

type Task struct {
	Title     string `json:"title"`
	SourceURL string `json:"source_url"`
	Spec      string `json:"spec"`
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.SourceURL) == "" && strings.TrimSpace(t.Spec) == "" {
		return errors.New("source_url or spec is required")
	}
	if len(t.Title) > 512 || len(t.SourceURL) > 4096 || len(t.Spec) > 256<<10 {
		return errors.New("task input exceeds size limit")
	}
	if t.SourceURL != "" {
		u, err := url.Parse(t.SourceURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return errors.New("source_url must be an HTTP(S) URL without credentials")
		}
	}
	return nil
}
func (t Task) Brief() string {
	if t.Spec == "" {
		return t.SourceURL
	}
	if t.SourceURL == "" {
		return t.Spec
	}
	return "Source: " + t.SourceURL + "\n\n" + t.Spec
}

type Artifact struct {
	ID          string     `json:"id"`
	JobID       string     `json:"task_id"`
	RunID       string     `json:"run_id"`
	Path        string     `json:"path"`
	ContentType string     `json:"content_type"`
	Size        int64      `json:"size"`
	Checksum    string     `json:"checksum"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiredAt   *time.Time `json:"expired_at,omitempty"`
}
type ArtifactLimits struct {
	MaxFileBytes int64 `json:"max_file_bytes"`
	MaxRunBytes  int64 `json:"max_run_bytes"`
}
