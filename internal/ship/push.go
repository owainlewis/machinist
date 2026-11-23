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
			if err := savePatch(ctx, options, log); err != nil {
				fmt.Fprintf(log, "machinist: the change was not saved as a patch: %v\n", err)
			}
		}
		return rejected
	}
	return nil
}

// savePatch writes the commits beyond the base branch to options.Patch, as
// git format-patch, for git am. format-patch skips merge commits, so when the
// branch has one, for example where the job merged the base branch in to
// resolve a conflict, the patch is instead one commit of everything the branch
// changed since it forked from the base branch. A patch that could not be made
// in full is removed, so a partial one is never taken for the change.
func savePatch(ctx context.Context, options PushOptions, log io.Writer) (err error) {
	commits := options.Base + "..HEAD"
	merges, err := gitOutput(ctx, options.Git, "rev-list", "--merges", "--count", commits)
	if err != nil {
		return err
	}
	if merges != "0" {
		fmt.Fprintf(log, "The branch has merge commits, so the patch is one commit of everything beyond %s.\n", options.Base)
		if commits, err = squash(ctx, options.Git, options.Base); err != nil {
			return err
		}
	}
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
	stderr, err := options.Git(ctx, file, "format-patch", "--stdout", commits)
	if err != nil {
		return errors.New(firstLine(stderr, err))
	}
	if info, err := file.Stat(); err != nil || info.Size() == 0 {
		return fmt.Errorf("the branch has no commits beyond %s", options.Base)
	}
	return nil
}

// squash makes one commit, on no branch, whose parent is where HEAD forked
// from base and whose tree is HEAD's, so it holds every change on the branch,
// merge resolutions included. It returns the range of that one commit. Its
// message is the messages of the branch's own commits, oldest first, and its
// author and committer are HEAD's author, so it needs no identity configured.
func squash(ctx context.Context, git Git, base string) (string, error) {
	fork, err := gitOutput(ctx, git, "merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	message, err := gitOutput(ctx, git, "log", "--reverse", "--no-merges", "--format=%B", base+"..HEAD")
	if err != nil {
		return "", err
	}
	if message == "" {
		if message, err = gitOutput(ctx, git, "log", "-1", "--format=%B", "HEAD"); err != nil {
			return "", err
		}
	}
	author, err := gitOutput(ctx, git, "log", "-1", "--format=%an%x00%ae", "HEAD")
	if err != nil {
		return "", err
	}
	name, email, _ := strings.Cut(author, "\x00")
	commit, err := gitOutput(ctx, git, "-c", "user.name="+name, "-c", "user.email="+email,
		"commit-tree", "HEAD^{tree}", "-p", fork, "-m", message)
	if err != nil {
		return "", err
	}
	return fork + ".." + commit, nil
}

// gitOutput runs git and returns what it printed, trimmed, or an error that
// says what git said.
func gitOutput(ctx context.Context, git Git, args ...string) (string, error) {
	var stdout strings.Builder
	stderr, err := git(ctx, &stdout, args...)
	if err != nil {
		return "", errors.New(firstLine(stderr, err))
	}
	return strings.TrimSpace(stdout.String()), nil
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
