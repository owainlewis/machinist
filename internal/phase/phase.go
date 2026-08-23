// Package phase loads the versioned prompt files that Factory dispatches.
//
// A phase is one markdown file under .factory/phases. Its frontmatter declares
// how the phase runs and its body is the prompt. A phase never declares what
// runs after it: the sequence is chosen at dispatch, so a phase file stays one
// prompt and Factory stays free of a pipeline router.
package phase

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Dir is the per-repository directory holding phase files.
const Dir = ".factory/phases"

// MaxPromptBytes bounds a rendered prompt. The control plane rejects anything
// larger, so failing here names the phase instead of returning an API error.
const MaxPromptBytes = 64 * 1024

// DefaultTimeout applies when a phase does not declare one.
const DefaultTimeout = 2 * time.Hour

// Phase is one loaded prompt file.
type Phase struct {
	// Name is the phase name. It defaults to the file name and must match it
	// when frontmatter sets it, so a dispatch always names the file on disk.
	Name string
	// Description says what the phase does. It is required: a phase file with
	// no description cannot be listed usefully.
	Description string
	// Runtime names a runtime declared in the operator configuration. Empty
	// means the configured default.
	Runtime string
	// Model overrides the runtime's model. Empty means the runtime default.
	Model string
	// Timeout bounds one run of this phase.
	Timeout time.Duration
	// ReadOnly reports whether the phase may change the working tree. Audit
	// phases set it so a prompt built from untrusted issue text cannot write.
	ReadOnly bool
	// Vars holds every frontmatter key, including ones Factory does not
	// interpret, and is addressable in the body as {{ phase.<key> }}.
	Vars map[string]string
	// Body is the unrendered prompt.
	Body string
	// Hash is the SHA-256 of the whole file. Freezing it on a run is what makes
	// "which prompt produced this pull request" answerable later.
	Hash string
	// Path is where the phase was loaded from.
	Path string
}

// ShortHash returns the display form of the content hash.
func (p Phase) ShortHash() string {
	if len(p.Hash) < 7 {
		return p.Hash
	}
	return p.Hash[:7]
}

// Load reads one phase by name from root.
func Load(root, name string) (Phase, error) {
	if err := validateName(name); err != nil {
		return Phase{}, err
	}
	path := filepath.Join(root, filepath.FromSlash(Dir), name+".md")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Phase{}, fmt.Errorf("phase %q not found: no file at %s", name, path)
	}
	if err != nil {
		return Phase{}, err
	}
	return parse(path, name, data)
}

// LoadAll reads every phase under root, sorted by name. A single unreadable
// phase fails the whole load so that `factory validate` reports a broken file
// rather than quietly listing the rest.
func LoadAll(root string) ([]Phase, error) {
	dir := filepath.Join(root, filepath.FromSlash(Dir))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no phase directory at %s", dir)
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}
	sort.Strings(names)
	phases := make([]Phase, 0, len(names))
	for _, name := range names {
		loaded, err := Load(root, name)
		if err != nil {
			return nil, err
		}
		phases = append(phases, loaded)
	}
	return phases, nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("a phase name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("phase name %q is longer than 64 characters", name)
	}
	for _, r := range name {
		if r == '-' || r == '_' || unicode.IsDigit(r) || (unicode.IsLetter(r) && r < utf8.RuneSelf) {
			continue
		}
		return fmt.Errorf("phase name %q may use only ASCII letters, digits, hyphen, and underscore", name)
	}
	return nil
}

func parse(path, name string, data []byte) (Phase, error) {
	sum := sha256.Sum256(data)
	fields, body, err := splitFrontmatter(string(data))
	if err != nil {
		return Phase{}, fmt.Errorf("%s: %w", path, err)
	}

	loaded := Phase{
		Name:    name,
		Timeout: DefaultTimeout,
		Vars:    fields,
		Body:    body,
		Hash:    hex.EncodeToString(sum[:]),
		Path:    path,
	}

	if declared, ok := fields["name"]; ok && declared != name {
		return Phase{}, fmt.Errorf("%s: frontmatter name %q does not match the file name %q", path, declared, name)
	}
	loaded.Description = fields["description"]
	if loaded.Description == "" {
		return Phase{}, fmt.Errorf("%s: description is required", path)
	}
	loaded.Runtime = fields["runtime"]
	loaded.Model = fields["model"]

	if raw, ok := fields["timeout"]; ok {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return Phase{}, fmt.Errorf("%s: timeout %q is not a duration such as 30m or 2h", path, raw)
		}
		if duration <= 0 {
			return Phase{}, fmt.Errorf("%s: timeout must be positive", path)
		}
		loaded.Timeout = duration
	}

	if raw, ok := fields["permissions"]; ok {
		switch raw {
		case "read-only":
			loaded.ReadOnly = true
		case "write":
		default:
			return Phase{}, fmt.Errorf("%s: permissions %q must be read-only or write", path, raw)
		}
	}

	if strings.TrimSpace(loaded.Body) == "" {
		return Phase{}, fmt.Errorf("%s: the phase body is empty, so there is no prompt to run", path)
	}
	return loaded, nil
}

// splitFrontmatter reads a leading --- delimited block of flat key: value pairs.
//
// The subset is deliberate. Frontmatter in this format is flat scalars in every
// real phase file, and parsing only that keeps Factory free of a YAML
// dependency. Nested structures are rejected with a message that says so rather
// than being silently ignored.
func splitFrontmatter(text string) (map[string]string, string, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, "", errors.New("the file must start with a --- frontmatter block")
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", errors.New("the frontmatter block is not closed by ---")
	}
	block := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	fields := map[string]string{}
	for number, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line != strings.TrimLeft(line, " \t") {
			return nil, "", fmt.Errorf("frontmatter line %d is indented; only flat key: value pairs are supported", number+1)
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, "", fmt.Errorf("frontmatter line %d is not a key: value pair", number+1)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, "", fmt.Errorf("frontmatter line %d has an empty key", number+1)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, "", fmt.Errorf("frontmatter key %q appears more than once", key)
		}
		fields[key] = unquote(strings.TrimSpace(value))
	}
	return fields, body, nil
}

func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			if unquoted, err := strconv.Unquote(value); err == nil {
				return unquoted
			}
			return value[1 : len(value)-1]
		}
	}
	return value
}
