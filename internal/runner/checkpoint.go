package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

const checkpointCommandTimeout = 30 * time.Second

var (
	errCheckpointTooLarge  = errors.New("checkpoint exceeds maximum size")
	defaultCheckpointBases = []string{"origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master"}
)

// canCheckpointStep reports whether a failure at failedIndex can save a
// resumable checkpoint: at least one earlier step must have succeeded, and if
// the preset has a checkout step, the failed step must come after checkout.
func canCheckpointStep(steps []Step, failedIndex int) bool {
	if failedIndex <= 0 || failedIndex >= len(steps) {
		return false
	}
	for index, step := range steps {
		if step.Name == "checkout" {
			return failedIndex > index
		}
	}
	return true
}

func stepIndex(steps []Step, name string) int {
	for index, step := range steps {
		if step.Name == name {
			return index
		}
	}
	return -1
}

// captureCheckpoint snapshots git commits beyond the remote base, uncommitted
// working tree changes, and $MACHINIST_DIR control files after a step failure.
// Returns nil if the total checkpoint exceeds protocol.MaxPatchBytes or if any
// git snapshot command fails in a git work tree.
func captureCheckpoint(workspace Workspace, branch, failedStep string) *protocol.Checkpoint {
	ctx, cancel := context.WithTimeout(context.Background(), checkpointCommandTimeout)
	defer cancel()

	cp := &protocol.Checkpoint{FailedStep: failedStep}
	if files := readCheckpointControlFiles(workspace.Control); len(files) > 0 {
		cp.ControlFiles = files
	}

	env := checkpointGitEnv(workspace.Environment)
	if isGitWorkTree(ctx, workspace.Directory, env) {
		headSHA, err := gitStdout(ctx, workspace.Directory, env, "rev-parse", "--verify", "HEAD")
		if err != nil || headSHA == "" {
			return nil
		}
		cp.HeadSHA = headSHA
		baseRef, patch, err := captureCommitPatch(ctx, workspace, env, branch)
		if err != nil {
			return nil
		}
		cp.BaseRef = baseRef
		if len(patch) > 0 {
			cp.Patch = patch
		}
		worktree, err := captureWorktreePatch(ctx, workspace.Directory, env)
		if err != nil {
			return nil
		}
		if len(worktree) > 0 {
			cp.Worktree = worktree
		}
	}

	if cp.SizeBytes() > protocol.MaxPatchBytes {
		return nil
	}
	return cp
}

// restoreCheckpoint restores control files and applies any saved git commit
// patch and uncommitted worktree diff into workspace.
func restoreCheckpoint(ctx context.Context, workspace Workspace, checkpoint *protocol.Checkpoint, log *eventLog) error {
	if checkpoint == nil {
		return nil
	}
	restoredFiles := 0
	for name, body := range checkpoint.ControlFiles {
		if !slices.Contains(protocol.CheckpointControlFiles, name) {
			continue
		}
		target := filepath.Join(workspace.Control, name)
		if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
			return fmt.Errorf("restore control file %s: %w", name, err)
		}
		restoredFiles++
	}

	needsGit := len(checkpoint.Patch) > 0 || len(checkpoint.Worktree) > 0 || checkpoint.BaseRef != ""
	if !needsGit {
		if log != nil && restoredFiles > 0 {
			_ = log.append("checkpoint.restored", "", fmt.Sprintf("files=%d", restoredFiles))
		}
		return nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, checkpointCommandTimeout)
	defer cancel()

	env := checkpointGitEnv(workspace.Environment)
	if !isGitWorkTree(cmdCtx, workspace.Directory, env) {
		return errors.New("workspace is not a git repository")
	}

	appliedCommits := false
	currentHead, err := gitStdout(cmdCtx, workspace.Directory, env, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return fmt.Errorf("read workspace HEAD: %w", err)
	}
	if checkpoint.HeadSHA == "" || currentHead != checkpoint.HeadSHA {
		if checkpoint.BaseRef != "" {
			baseSHA, err := resolveRestoreBase(cmdCtx, workspace.Directory, env, checkpoint.BaseRef)
			if err != nil {
				return fmt.Errorf("resolve checkpoint base %s: %w", checkpoint.BaseRef, err)
			}
			if baseSHA != "" && currentHead != baseSHA {
				if err := runGitWithInput(cmdCtx, workspace.Directory, env, nil, "reset", "--hard", baseSHA); err != nil {
					return fmt.Errorf("reset branch to %s: %w", checkpoint.BaseRef, err)
				}
			}
		}
		if len(checkpoint.Patch) > 0 {
			if err := runGitWithInput(cmdCtx, workspace.Directory, env, checkpoint.Patch, "am", "--3way"); err != nil {
				_ = runGitWithInput(cmdCtx, workspace.Directory, env, nil, "am", "--abort")
				return fmt.Errorf("apply checkpoint commits: %w", err)
			}
			appliedCommits = true
		}
	}

	appliedWorktree := false
	if len(checkpoint.Worktree) > 0 {
		if err := runGitWithInput(cmdCtx, workspace.Directory, env, checkpoint.Worktree, "apply", "--whitespace=nowarn"); err != nil {
			return fmt.Errorf("apply checkpoint uncommitted changes: %w", err)
		}
		appliedWorktree = true
	}

	if log != nil {
		_ = log.append("checkpoint.restored", "", fmt.Sprintf("commits=%t worktree=%t files=%d", appliedCommits, appliedWorktree, restoredFiles))
	}
	return nil
}

func readCheckpointControlFiles(control string) map[string]string {
	files := map[string]string{}
	for _, name := range protocol.CheckpointControlFiles {
		path := filepath.Join(control, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxControlFile {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil || len(body) == 0 {
			continue
		}
		files[name] = string(body)
	}
	return files
}

func checkpointGitEnv(base []string) []string {
	env := SanitizedEnvironment(base)
	hasAuthorName, hasAuthorEmail := false, false
	hasCommitterName, hasCommitterEmail := false, false
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "GIT_AUTHOR_NAME="):
			hasAuthorName = strings.TrimPrefix(entry, "GIT_AUTHOR_NAME=") != ""
		case strings.HasPrefix(entry, "GIT_AUTHOR_EMAIL="):
			hasAuthorEmail = strings.TrimPrefix(entry, "GIT_AUTHOR_EMAIL=") != ""
		case strings.HasPrefix(entry, "GIT_COMMITTER_NAME="):
			hasCommitterName = strings.TrimPrefix(entry, "GIT_COMMITTER_NAME=") != ""
		case strings.HasPrefix(entry, "GIT_COMMITTER_EMAIL="):
			hasCommitterEmail = strings.TrimPrefix(entry, "GIT_COMMITTER_EMAIL=") != ""
		}
	}
	if !hasAuthorName {
		env = append(env, "GIT_AUTHOR_NAME=machinist")
	}
	if !hasAuthorEmail {
		env = append(env, "GIT_AUTHOR_EMAIL=machinist@localhost")
	}
	if !hasCommitterName {
		env = append(env, "GIT_COMMITTER_NAME=machinist")
	}
	if !hasCommitterEmail {
		env = append(env, "GIT_COMMITTER_EMAIL=machinist@localhost")
	}
	return env
}

func isGitWorkTree(ctx context.Context, dir string, env []string) bool {
	out, err := gitStdout(ctx, dir, env, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

func isGitAncestor(ctx context.Context, dir string, env []string, ancestor, descendant string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return false, errors.New(msg)
}

func resolveCheckpointBase(ctx context.Context, dir string, env []string, branch string) (string, error) {
	// If the job's branch already exists on origin and is an ancestor of HEAD,
	// only capture commits beyond origin/<branch> so checkout + git am fast-forwards
	// cleanly without replaying commits already on the remote branch.
	if branch != "" {
		remoteBranch := "refs/remotes/origin/" + branch
		if _, err := gitStdout(ctx, dir, env, "rev-parse", "--verify", remoteBranch); err == nil {
			isAncestor, err := isGitAncestor(ctx, dir, env, remoteBranch, "HEAD")
			if err != nil {
				return "", err
			}
			if isAncestor {
				return remoteBranch, nil
			}
		}
	}
	for _, candidate := range defaultCheckpointBases {
		if _, err := gitStdout(ctx, dir, env, "rev-parse", "--verify", candidate); err == nil {
			return candidate, nil
		}
	}
	return "", nil
}

func resolveRestoreBase(ctx context.Context, dir string, env []string, baseRef string) (string, error) {
	if baseRef == "" || strings.HasPrefix(baseRef, "-") || strings.ContainsAny(baseRef, " \t\r\n\x00") {
		return "", fmt.Errorf("invalid base ref %q", baseRef)
	}
	if sha, err := gitStdout(ctx, dir, env, "rev-parse", "--verify", baseRef+"^{commit}"); err == nil && sha != "" {
		return sha, nil
	}
	if slices.Contains(defaultCheckpointBases, baseRef) {
		for _, candidate := range defaultCheckpointBases {
			if sha, err := gitStdout(ctx, dir, env, "rev-parse", "--verify", candidate+"^{commit}"); err == nil && sha != "" {
				return sha, nil
			}
		}
	}
	return "", fmt.Errorf("cannot resolve base ref %q", baseRef)
}

func captureCommitPatch(ctx context.Context, workspace Workspace, env []string, branch string) (string, []byte, error) {
	base, err := resolveCheckpointBase(ctx, workspace.Directory, env, branch)
	if err != nil {
		return "", nil, err
	}
	if base == "" {
		if saved := savedPatch(workspace.Control); len(saved) > 0 {
			return "", saved, nil
		}
		return "", nil, nil
	}
	ahead, err := gitStdout(ctx, workspace.Directory, env, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return "", nil, err
	}
	if ahead == "" || ahead == "0" {
		return base, nil, nil
	}
	if base == "origin/HEAD" {
		if saved := savedPatch(workspace.Control); len(saved) > 0 {
			return base, saved, nil
		}
	}
	commitRange := base + "..HEAD"
	merges, err := gitStdout(ctx, workspace.Directory, env, "rev-list", "--merges", "--count", commitRange)
	if err != nil {
		return "", nil, err
	}
	if merges != "0" {
		commitRange, err = squashCheckpointRange(ctx, workspace.Directory, env, base)
		if err != nil {
			return "", nil, err
		}
	}
	patch, err := gitStdoutLimited(ctx, workspace.Directory, env, protocol.MaxPatchBytes, "format-patch", "--stdout", "--binary", commitRange)
	if err != nil {
		return "", nil, err
	}
	return base, patch, nil
}

func squashCheckpointRange(ctx context.Context, dir string, env []string, base string) (string, error) {
	fork, err := gitStdout(ctx, dir, env, "merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	message, err := gitStdout(ctx, dir, env, "log", "--reverse", "--no-merges", "--format=%B", base+"..HEAD")
	if err != nil {
		return "", err
	}
	if message == "" {
		if message, err = gitStdout(ctx, dir, env, "log", "-1", "--format=%B", "HEAD"); err != nil {
			return "", err
		}
	}
	author, err := gitStdout(ctx, dir, env, "log", "-1", "--format=%an%x00%ae", "HEAD")
	if err != nil {
		return "", err
	}
	name, email, _ := strings.Cut(author, "\x00")
	commit, err := gitStdout(ctx, dir, env, "-c", "user.name="+name, "-c", "user.email="+email,
		"commit-tree", "HEAD^{tree}", "-p", fork, "-m", message)
	if err != nil {
		return "", err
	}
	return fork + ".." + commit, nil
}

func captureWorktreePatch(ctx context.Context, dir string, env []string) ([]byte, error) {
	status, err := gitStdout(ctx, dir, env, "status", "--porcelain")
	if err != nil || status == "" {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "machinist-checkpoint-idx-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	indexEnv := append(append([]string(nil), env...), "GIT_INDEX_FILE="+filepath.Join(tmpDir, "index"))
	if _, err := gitStdout(ctx, dir, indexEnv, "read-tree", "HEAD"); err != nil {
		return nil, err
	}
	if _, err := gitStdout(ctx, dir, indexEnv, "add", "-A"); err != nil {
		return nil, err
	}
	return gitStdoutLimited(ctx, dir, indexEnv, protocol.MaxPatchBytes, "diff", "--cached", "--binary", "HEAD")
}

func gitStdout(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

type boundedBuffer struct {
	limit    int
	buf      bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.exceeded || b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, errCheckpointTooLarge
	}
	return b.buf.Write(p)
}

func gitStdoutLimited(ctx context.Context, dir string, env []string, limit int, args ...string) ([]byte, error) {
	stdout := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, errCheckpointTooLarge
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return stdout.buf.Bytes(), nil
}

func runGitWithInput(ctx context.Context, dir string, env []string, input []byte, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
