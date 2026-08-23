package phase

import (
	"fmt"
	"sort"
	"strings"
)

// Context supplies the values a phase body may reference.
//
// There are exactly three namespaces. Run always exists. Issue exists only when
// the run came from a tracker, so a repository-targeted phase such as audit must
// not reference it. Phase exposes the phase's own frontmatter, which is how a
// phase declares a bound such as max_findings without Factory modelling it.
type Context struct {
	Run   map[string]string
	Issue map[string]string
	Phase map[string]string
}

// Render substitutes {{ namespace.key }} references in the phase body.
//
// Rendering is strict. An unknown namespace, an unknown key, or a reference to
// issue.* on a run with no issue is an error. A silently empty substitution
// would hand an agent a prompt with no acceptance criteria and no way to tell,
// which is the failure this strictness exists to prevent.
func (p Phase) Render(context Context) (string, error) {
	if context.Phase == nil {
		context.Phase = p.Vars
	}
	var out strings.Builder
	out.Grow(len(p.Body))

	rest := p.Body
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:start])
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return "", fmt.Errorf("%s: unclosed {{ in the phase body", p.Path)
		}
		reference := strings.TrimSpace(rest[:end])
		rest = rest[end+2:]

		value, err := context.lookup(reference)
		if err != nil {
			return "", fmt.Errorf("%s: %w", p.Path, err)
		}
		out.WriteString(value)
	}

	rendered := out.String()
	if len(rendered) > MaxPromptBytes {
		return "", fmt.Errorf("%s: the rendered prompt is %d bytes, over the %d byte limit", p.Path, len(rendered), MaxPromptBytes)
	}
	if strings.TrimSpace(rendered) == "" {
		return "", fmt.Errorf("%s: the rendered prompt is empty", p.Path)
	}
	return rendered, nil
}

func (c Context) lookup(reference string) (string, error) {
	namespace, key, ok := strings.Cut(reference, ".")
	if !ok {
		return "", fmt.Errorf("{{ %s }} must be namespaced, such as {{ run.title }}", reference)
	}
	namespace = strings.TrimSpace(namespace)
	key = strings.TrimSpace(key)

	var values map[string]string
	switch namespace {
	case "run":
		values = c.Run
	case "issue":
		if c.Issue == nil {
			return "", fmt.Errorf("{{ %s }} needs an issue, but this run targets a repository", reference)
		}
		values = c.Issue
	case "phase":
		values = c.Phase
	default:
		return "", fmt.Errorf("{{ %s }} uses unknown namespace %q; use run, issue, or phase", reference, namespace)
	}

	value, ok := values[key]
	if !ok {
		return "", fmt.Errorf("{{ %s }} is not set; %s has %s", reference, namespace, known(values))
	}
	return value, nil
}

func known(values map[string]string) string {
	if len(values) == 0 {
		return "no values"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
