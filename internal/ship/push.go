package ship

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Git runs git with these arguments in the workspace and returns what it
// printed on stderr.
type Git func(ctx context.Context, args ...string) (stderr string, err error)

// PushOptions configures one push.
type PushOptions struct {
	Git Git
	// Log gets git's own output, so the step log shows all of it.
	Log io.Writer
}

// maxReason caps how much of git's answer a rejection carries.
const maxReason = 500

// PushRejectedError means the push failed, so the job's work never reached
// GitHub. Reason is what git said about it.
type PushRejectedError struct{ Reason string }

func (err *PushRejectedError) Error() string {
	return "GitHub rejected the push: " + err.Reason
}

// Push pushes the job's branch to origin. A push that fails is an error that
// says what GitHub answered, so the step and the job fail with it.
func Push(ctx context.Context, options PushOptions) error {
	log := options.Log
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintln(log, "▸ Push the branch")
	stderr, err := options.Git(ctx, "push", "-u", "origin", "HEAD")
	if stderr != "" {
		fmt.Fprint(log, stderr)
	}
	if err != nil {
		return &PushRejectedError{Reason: pushReason(stderr, err)}
	}
	return nil
}

// pushReason picks the lines of git's answer that say why the push failed:
// each refused ref, with the reason GitHub gave, and what the remote said.
// git's closing "failed to push some refs" says nothing new, so it is left out.
func pushReason(stderr string, err error) string {
	var refused, said []string
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "!"):
			refused = append(refused, line)
		case strings.HasPrefix(line, "error: failed to push some refs"):
		case strings.HasPrefix(line, "remote:"):
			if strings.TrimSpace(strings.TrimPrefix(line, "remote:")) != "" {
				said = append(said, line)
			}
		case strings.HasPrefix(line, "error:"), strings.HasPrefix(line, "fatal:"):
			said = append(said, line)
		}
	}
	reason := strings.Join(append(refused, said...), "; ")
	if reason == "" {
		reason = firstLine(stderr, err)
	}
	if len(reason) > maxReason {
		reason = strings.ToValidUTF8(reason[:maxReason], "") + "…"
	}
	return reason
}
