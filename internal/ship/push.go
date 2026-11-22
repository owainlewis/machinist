package ship

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Git runs git with these arguments in the workspace, writes what it printed
// on stdout to stdout, and returns what it printed on stderr.
type Git func(ctx context.Context, stdout io.Writer, args ...string) (stderr string, err error)

// PushOptions configures one push.
type PushOptions struct {
	Git Git
	// Log gets git's own output, so the step log shows all of it.
	Log io.Writer
	// Patch is where a rejected push saves the change, so it is not lost
	// with the workspace. Empty saves nothing.
	Patch string
	// Base is what the patch is made against: the base branch.
	Base string
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
// says what GitHub answered, so the step and the job fail with it. It first
// saves the change as a patch against the base branch, so the work survives
// the workspace.
func Push(ctx context.Context, options PushOptions) error {
	log := options.Log
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintln(log, "▸ Push the branch")
	stderr, err := options.Git(ctx, io.Discard, "push", "-u", "origin", "HEAD")
	if stderr != "" {
		fmt.Fprint(log, stderr)
	}
	if err != nil {
		rejected := &PushRejectedError{Reason: pushReason(stderr, err)}
		if options.Patch != "" {
			fmt.Fprintln(log, "▸ Save the change as a patch")
			if err := savePatch(ctx, options); err != nil {
				fmt.Fprintf(log, "machinist: the change was not saved as a patch: %v\n", err)
			}
		}
		return rejected
	}
	return nil
}

// savePatch writes git format-patch of the commits beyond the base branch to
// options.Patch. A patch that could not be made in full is removed, so a
// partial one is never taken for the change.
func savePatch(ctx context.Context, options PushOptions) (err error) {
	file, err := os.OpenFile(options.Patch, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(options.Patch)
		}
	}()
	stderr, err := options.Git(ctx, file, "format-patch", "--stdout", options.Base+"..HEAD")
	if err != nil {
		return errors.New(firstLine(stderr, err))
	}
	if info, err := file.Stat(); err != nil || info.Size() == 0 {
		return fmt.Errorf("the branch has no commits beyond %s", options.Base)
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
