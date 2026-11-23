package ship

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGit(stderr string, err error, calls *[]string) Git {
	return func(_ context.Context, _ io.Writer, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		return stderr, err
	}
}

// patchGit rejects the push, finds no merge commits and answers format-patch
// with patch, or with formatErr.
func patchGit(patch string, formatErr error, calls *[]string) Git {
	return func(_ context.Context, stdout io.Writer, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if args[0] == "push" {
			return " ! [remote rejected] HEAD -> machinist/t_1 (no workflows)\n", errors.New("exit status 1")
		}
		if args[0] == "rev-list" {
			io.WriteString(stdout, "0\n")
			return "", nil
		}
		io.WriteString(stdout, patch)
		if formatErr != nil {
			return "fatal: bad revision 'origin/HEAD..HEAD'\n", formatErr
		}
		return "", nil
	}
}

func TestPushPushesTheBranch(t *testing.T) {
	var calls []string
	var log bytes.Buffer
	if err := Push(t.Context(), PushOptions{Git: fakeGit("", nil, &calls), Log: &log}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "push -u origin HEAD" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestPushFailsWithWhatGitHubAnswered(t *testing.T) {
	for _, test := range []struct {
		name, stderr, want string
	}{
		{
			"workflow scope",
			"To https://github.com/o/r.git\n ! [remote rejected] HEAD -> machinist/t_1 (refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission)\nerror: failed to push some refs to 'https://github.com/o/r.git'\n",
			"GitHub rejected the push: ! [remote rejected] HEAD -> machinist/t_1 (refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission)",
		},
		{
			"hook with a message",
			"remote: no workflows        \nremote: \nTo /tmp/o.git\n ! [remote rejected] HEAD -> machinist/t_1 (pre-receive hook declined)\nerror: failed to push some refs to '/tmp/o.git'\n",
			"GitHub rejected the push: ! [remote rejected] HEAD -> machinist/t_1 (pre-receive hook declined); remote: no workflows",
		},
		{
			"no access",
			"remote: Permission to o/r.git denied to bot.\nfatal: unable to access 'https://github.com/o/r.git/': The requested URL returned error: 403\n",
			"GitHub rejected the push: remote: Permission to o/r.git denied to bot.; fatal: unable to access 'https://github.com/o/r.git/': The requested URL returned error: 403",
		},
		{"nothing said", "", "GitHub rejected the push: exit status 128"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			var log bytes.Buffer
			err := Push(t.Context(), PushOptions{Git: fakeGit(test.stderr, errors.New("exit status 128"), &calls), Log: &log})
			var rejected *PushRejectedError
			if !errors.As(err, &rejected) || err.Error() != test.want {
				t.Fatalf("err = %v\nwant %s", err, test.want)
			}
			if !strings.Contains(log.String(), test.stderr) {
				t.Fatalf("the log left out git's answer:\n%s", log.String())
			}
		})
	}
}

func TestPushCapsALongAnswer(t *testing.T) {
	var calls []string
	err := Push(t.Context(), PushOptions{Git: fakeGit(" ! [rejected] "+strings.Repeat("é", 400)+"\n", errors.New("exit status 1"), &calls)})
	var rejected *PushRejectedError
	if !errors.As(err, &rejected) || len(rejected.Reason) > maxReason+len("…") || !strings.HasSuffix(rejected.Reason, "…") {
		t.Fatalf("reason = %q", rejected.Reason)
	}
}

func TestARejectedPushSavesTheChangeAsAPatch(t *testing.T) {
	var calls []string
	var log bytes.Buffer
	path := filepath.Join(t.TempDir(), "change.patch")
	patch := "From 1234 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] Use Postgres\n"
	err := Push(t.Context(), PushOptions{Git: patchGit(patch, nil, &calls), Log: &log, Patch: path, Base: "origin/HEAD"})
	var rejected *PushRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(calls, "; ") != "push -u origin HEAD; rev-list --merges --count origin/HEAD..HEAD; format-patch --stdout origin/HEAD..HEAD" {
		t.Fatalf("calls = %v", calls)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != patch {
		t.Fatalf("patch = %q, %v", body, err)
	}
	if !strings.Contains(log.String(), "Save the change as a patch") {
		t.Fatalf("log = %s", log.String())
	}
}

func TestAPatchThatCannotBeMadeIsNotLeftBehind(t *testing.T) {
	for _, test := range []struct {
		name, patch string
		err         error
		said        string
	}{
		{"format-patch fails", "From 1234 partial", errors.New("exit status 128"), "bad revision"},
		{"no commits", "", nil, "no commits beyond origin/HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			var log bytes.Buffer
			path := filepath.Join(t.TempDir(), "change.patch")
			err := Push(t.Context(), PushOptions{Git: patchGit(test.patch, test.err, &calls), Log: &log, Patch: path, Base: "origin/HEAD"})
			var rejected *PushRejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the patch was left behind: %v", err)
			}
			if !strings.Contains(log.String(), "not saved as a patch: ") || !strings.Contains(log.String(), test.said) {
				t.Fatalf("log = %s", log.String())
			}
		})
	}
}

func TestAPushThatWorksSavesNoPatch(t *testing.T) {
	var calls []string
	path := filepath.Join(t.TempDir(), "change.patch")
	if err := Push(t.Context(), PushOptions{Git: fakeGit("", nil, &calls), Patch: path, Base: "origin/HEAD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(calls) != 1 {
		t.Fatalf("calls = %v, stat = %v", calls, err)
	}
}
