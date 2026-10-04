package factory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func completeGitNames(raw string, truncated bool) []string {
	if truncated {
		end := strings.LastIndexByte(raw, 0)
		if end < 0 {
			return []string{}
		}
		raw = raw[:end+1]
	}
	if raw == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
}

// Tracked and untracked recovery evidence share one output budget.
func (s *Service) reviewChanges(project, directory, base string) (string, bool, []string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	diff, diffTruncated, err := s.gitLimitedContext(ctx, project, directory, 1<<20, false, "diff", base)
	if err != nil {
		return "", false, nil, false, err
	}
	tracked, filesTruncated, err := s.gitLimitedContext(ctx, project, directory, 64<<10, false, "diff", "--name-only", "-z", base)
	if err != nil {
		return "", false, nil, false, err
	}
	untracked, untrackedTruncated, err := s.gitLimitedContext(ctx, project, directory, (64<<10)-len(tracked), false, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", false, nil, false, err
	}
	names := completeGitNames(tracked, filesTruncated)
	others := completeGitNames(untracked, untrackedTruncated)
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range others {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	filesTruncated = filesTruncated || untrackedTruncated
	for _, name := range others {
		if diffTruncated {
			break
		}
		left := (1 << 20) - len(diff)
		if left <= 0 {
			diffTruncated = true
			break
		}
		// ls-files represents an untracked nested repository as a directory.
		if strings.HasSuffix(name, "/") {
			metadata := fmt.Sprintf("\n[Untracked directory or nested repository: %q. Contents are not rendered; inspect it separately before approval.]\n", name)
			if len(metadata) > left {
				diff += strings.ToValidUTF8(metadata[:left], "")
				diffTruncated = true
			} else {
				diff += metadata
			}
			continue
		}
		// Git renders a symlink's link text, rather than reading its target.
		added, truncated, e := s.gitLimitedContext(ctx, project, directory, left, true, "diff", "--no-index", "--", "/dev/null", name)
		if e != nil {
			if ctx.Err() != nil {
				diffTruncated = true
				break
			}
			return "", false, nil, false, e
		}
		diff += added
		diffTruncated = diffTruncated || truncated
	}
	if untrackedTruncated {
		diffTruncated = true
	}
	if diffTruncated {
		diff += "\n[Output truncated: review exceeds its output or time limit. Inspect the full change in the repository before approval.]"
	}
	return diff, diffTruncated, names, filesTruncated, nil
}
