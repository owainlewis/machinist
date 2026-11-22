package ship

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func fakeGit(stderr string, err error, calls *[]string) Git {
	return func(_ context.Context, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		return stderr, err
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
