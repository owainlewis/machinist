package githubauth

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func parseEnv(t *testing.T, env []string) map[string]string {
	t.Helper()
	parsed := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed env entry %q: missing '='", entry)
		}
		if _, exists := parsed[key]; exists {
			t.Fatalf("duplicate env key %q", key)
		}
		parsed[key] = value
	}
	return parsed
}

func parseGitConfig(t *testing.T, parsed map[string]string) [][2]string {
	t.Helper()
	rawCount, ok := parsed["GIT_CONFIG_COUNT"]
	if !ok {
		t.Fatal("missing GIT_CONFIG_COUNT")
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil {
		t.Fatalf("invalid GIT_CONFIG_COUNT %q: %v", rawCount, err)
	}
	pairs := make([][2]string, count)
	for i := range count {
		keyName := "GIT_CONFIG_KEY_" + strconv.Itoa(i)
		valName := "GIT_CONFIG_VALUE_" + strconv.Itoa(i)
		key, ok := parsed[keyName]
		if !ok {
			t.Fatalf("missing %s", keyName)
		}
		val, ok := parsed[valName]
		if !ok {
			t.Fatalf("missing %s", valName)
		}
		pairs[i] = [2]string{key, val}
	}
	for k := range parsed {
		if strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			idxStr := k[strings.LastIndex(k, "_")+1:]
			idx, err := strconv.Atoi(idxStr)
			if err != nil || idx < 0 || idx >= count {
				t.Fatalf("unexpected git config env var %q for GIT_CONFIG_COUNT=%d", k, count)
			}
		}
	}
	return pairs
}

func extractCredentialHelper(t *testing.T, pairs [][2]string) string {
	t.Helper()
	for _, pair := range pairs {
		if pair[0] == "credential.helper" && pair[1] != "" {
			return pair[1]
		}
	}
	t.Fatal("missing non-empty credential.helper entry")
	return ""
}

func TestGitHubEnvironmentValid(t *testing.T) {
	const (
		token      = "ghs_secret_token_12345"
		repository = "owainlewis/machinist"
	)

	env, err := GitHubEnvironment(token, repository)
	if err != nil {
		t.Fatalf("GitHubEnvironment(%q, %q) unexpected error: %v", token, repository, err)
	}

	parsed := parseEnv(t, env)

	wantStatic := map[string]string{
		"GH_TOKEN":                    token,
		"GITHUB_TOKEN":                token,
		"GH_HOST":                     "github.com",
		"MACHINIST_GITHUB_REPOSITORY": repository,
		"GIT_TERMINAL_PROMPT":         "0",
		"GIT_ASKPASS":                 "",
		"SSH_ASKPASS":                 "",
	}
	for key, want := range wantStatic {
		got, ok := parsed[key]
		if !ok {
			t.Errorf("missing env key %q", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	pairs := parseGitConfig(t, parsed)
	helper := extractCredentialHelper(t, pairs)
	if !strings.HasPrefix(helper, "!") {
		t.Fatalf("credential helper %q must start with '!' shell prefix", helper)
	}

	wantPairs := [][2]string{
		{"credential.helper", ""},
		{"credential.helper", helper},
		{"credential.https://github.com.helper", ""},
		{"credential.https://github.com.helper", helper},
		{"credential.useHttpPath", "true"},
		{"http.extraHeader", ""},
		{"http.https://github.com/.extraHeader", ""},
		{"url.https://github.com/.insteadOf", "git@github.com:"},
		{"url.https://github.com/.insteadOf", "ssh://git@github.com/"},
	}
	if !slices.Equal(pairs, wantPairs) {
		t.Fatalf("git config pairs =\n%#v\nwant\n%#v", pairs, wantPairs)
	}

	// Ensure no extra unexpected environment variables were emitted.
	wantEnvLen := len(wantStatic) + 1 + 2*len(wantPairs)
	if len(env) != wantEnvLen {
		t.Errorf("len(env) = %d, want %d", len(env), wantEnvLen)
	}

	// Token must never be embedded in Git configuration entries.
	for i, pair := range pairs {
		if strings.Contains(pair[0], token) || strings.Contains(pair[1], token) {
			t.Errorf("git config entry %d (%q) leaks token", i, pair[0])
		}
	}
}

func TestGitHubEnvironmentValidRepositories(t *testing.T) {
	validRepos := []string{
		"owner/repo",
		"a/b",
		"My-Org_1/My.Repo-2_3",
		"org.name/repo.name.v2",
		".hidden/repo",
		"owner/.repo",
		"owner/repo..v1",
		"owner/..repo",
		"owner/repo.",
		"123/456",
	}

	for _, repo := range validRepos {
		t.Run(repo, func(t *testing.T) {
			env, err := GitHubEnvironment("ghs_valid_token", repo)
			if err != nil {
				t.Fatalf("GitHubEnvironment with repo %q returned error: %v", repo, err)
			}
			parsed := parseEnv(t, env)
			if got := parsed["MACHINIST_GITHUB_REPOSITORY"]; got != repo {
				t.Errorf("MACHINIST_GITHUB_REPOSITORY = %q, want %q", got, repo)
			}
		})
	}
}

func TestGitHubEnvironmentRejectsInvalidTokens(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "carriage return", token: "ghs_abc\rdef"},
		{name: "leading carriage return", token: "\rghs_abc"},
		{name: "trailing carriage return", token: "ghs_abc\r"},
		{name: "newline", token: "ghs_abc\ndef"},
		{name: "leading newline", token: "\nghs_abc"},
		{name: "trailing newline", token: "ghs_abc\n"},
		{name: "crlf", token: "ghs_abc\r\ndef"},
		{name: "nul byte", token: "ghs_abc\x00def"},
		{name: "leading nul byte", token: "\x00ghs_abc"},
		{name: "trailing nul byte", token: "ghs_abc\x00"},
		{name: "only newline", token: "\n"},
		{name: "only nul byte", token: "\x00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := GitHubEnvironment(tc.token, "owner/repo")
			if err == nil {
				t.Fatalf("expected error for token %q, got env %v", tc.token, env)
			}
			if env != nil {
				t.Errorf("expected nil env on error, got %v", env)
			}
		})
	}
}

func TestGitHubEnvironmentRejectsInvalidRepositories(t *testing.T) {
	cases := []struct {
		name       string
		repository string
		wantErr    string
	}{
		// Missing or malformed slashes
		{name: "empty", repository: "", wantErr: "owner/repository"},
		{name: "missing slash", repository: "owner", wantErr: "owner/repository"},
		{name: "only slash", repository: "/", wantErr: "owner/repository"},
		{name: "missing owner", repository: "/repo", wantErr: "owner/repository"},
		{name: "missing repo", repository: "owner/", wantErr: "owner/repository"},
		{name: "extra slash", repository: "owner/repo/extra", wantErr: "owner/repository"},
		{name: "double slash", repository: "owner//repo", wantErr: "owner/repository"},
		{name: "leading slash with two segments", repository: "/owner/repo", wantErr: "owner/repository"},
		{name: "trailing slash with two segments", repository: "owner/repo/", wantErr: "owner/repository"},

		// Disallowed characters
		{name: "space in repo", repository: "owner/repo name", wantErr: "owner/repository"},
		{name: "leading space", repository: " owner/repo", wantErr: "owner/repository"},
		{name: "trailing space", repository: "owner/repo ", wantErr: "owner/repository"},
		{name: "newline in repo", repository: "owner/repo\n", wantErr: "owner/repository"},
		{name: "tab in repo", repository: "owner/\trepo", wantErr: "owner/repository"},
		{name: "full https url", repository: "https://github.com/owner/repo", wantErr: "owner/repository"},
		{name: "ssh url", repository: "git@github.com:owner/repo", wantErr: "owner/repository"},
		{name: "semicolon injection", repository: "owner/repo;id", wantErr: "owner/repository"},
		{name: "dollar expansion", repository: "owner/$repo", wantErr: "owner/repository"},
		{name: "backtick injection", repository: "owner/`id`", wantErr: "owner/repository"},
		{name: "at sign", repository: "owner/repo@main", wantErr: "owner/repository"},
		{name: "colon", repository: "owner:repo/name", wantErr: "owner/repository"},
		{name: "question mark", repository: "owner/repo?foo=bar", wantErr: "owner/repository"},
		{name: "hash fragment", repository: "owner/repo#ref", wantErr: "owner/repository"},
		{name: "backslash", repository: `owner\repo/name`, wantErr: "owner/repository"},
		{name: "nul byte", repository: "owner/repo\x00", wantErr: "owner/repository"},
		{name: "non-ascii", repository: "ownér/repo", wantErr: "owner/repository"},

		// Path traversal segments
		{name: "dot owner", repository: "./repo", wantErr: "invalid GitHub repository"},
		{name: "dotdot owner", repository: "../repo", wantErr: "invalid GitHub repository"},
		{name: "dot repo", repository: "owner/.", wantErr: "invalid GitHub repository"},
		{name: "dotdot repo", repository: "owner/..", wantErr: "invalid GitHub repository"},
		{name: "both dot", repository: "./.", wantErr: "invalid GitHub repository"},
		{name: "both dotdot", repository: "../..", wantErr: "invalid GitHub repository"},
		{name: "dot and dotdot", repository: "./..", wantErr: "invalid GitHub repository"},
		{name: "dotdot and dot", repository: "../.", wantErr: "invalid GitHub repository"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := GitHubEnvironment("ghs_valid_token", tc.repository)
			if err == nil {
				t.Fatalf("expected error for repository %q, got env %v", tc.repository, env)
			}
			if env != nil {
				t.Errorf("expected nil env on error, got %v", env)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func runShellHelper(t *testing.T, env []string, helper, action, stdin string) (string, error) {
	t.Helper()
	script := strings.TrimPrefix(helper, "!")
	// Git invokes "!..." helpers as `sh -c '<cmd> "$@"' - <action>`.
	cmd := exec.Command("sh", "-c", script+` "$@"`, "-", action)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), err
}

func TestCredentialHelperShellSnippetScoping(t *testing.T) {
	const (
		// Include shell & printf metacharacters to verify quoting and %s formatting.
		token      = `ghs_tok%s$HOME'"\=123`
		repository = "owainlewis/machinist"
	)

	env, err := GitHubEnvironment(token, repository)
	if err != nil {
		t.Fatalf("GitHubEnvironment error: %v", err)
	}
	parsed := parseEnv(t, env)
	pairs := parseGitConfig(t, parsed)
	helper := extractCredentialHelper(t, pairs)

	wantOutput := "username=x-access-token\npassword=" + token + "\n"

	allowedCases := []struct {
		name  string
		stdin string
	}{
		{
			name:  "exact repository path",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "repository path with .git suffix",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/machinist.git\n\n",
		},
		{
			name:  "ignores unrelated credential fields",
			stdin: "username=ignored\nprotocol=https\nfoo=bar\nhost=github.com\npath=owainlewis/machinist\n\n",
		},
	}
	for _, tc := range allowedCases {
		t.Run("allows/"+tc.name, func(t *testing.T) {
			out, err := runShellHelper(t, env, helper, "get", tc.stdin)
			if err != nil {
				t.Fatalf("helper get failed: %v (stdout=%q)", err, out)
			}
			if out != wantOutput {
				t.Errorf("stdout = %q, want %q", out, wantOutput)
			}
		})
	}

	deniedCases := []struct {
		name  string
		stdin string
	}{
		{
			name:  "http protocol",
			stdin: "protocol=http\nhost=github.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "ssh protocol",
			stdin: "protocol=ssh\nhost=github.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "missing protocol",
			stdin: "host=github.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "other host",
			stdin: "protocol=https\nhost=evil.example\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "host suffix spoof",
			stdin: "protocol=https\nhost=github.com.evil.example\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "subdomain of github.com",
			stdin: "protocol=https\nhost=api.github.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "uppercase host",
			stdin: "protocol=https\nhost=GITHUB.COM\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "missing host",
			stdin: "protocol=https\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "different repository same owner",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/other\n\n",
		},
		{
			name:  "different owner same repository",
			stdin: "protocol=https\nhost=github.com\npath=attacker/machinist\n\n",
		},
		{
			name:  "prefix match repository",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/machinist-extra\n\n",
		},
		{
			name:  "subpath under repository",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/machinist/info/refs\n\n",
		},
		{
			name:  "double .git suffix",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/machinist.git.git\n\n",
		},
		{
			name:  "different case repository",
			stdin: "protocol=https\nhost=github.com\npath=OWAINLEWIS/MACHINIST\n\n",
		},
		{
			name:  "missing path",
			stdin: "protocol=https\nhost=github.com\n\n",
		},
		{
			name:  "empty input",
			stdin: "",
		},
	}
	for _, tc := range deniedCases {
		t.Run("denies/"+tc.name, func(t *testing.T) {
			out, err := runShellHelper(t, env, helper, "get", tc.stdin)
			if err == nil {
				t.Fatalf("expected helper to reject %q, but it exited 0 with stdout=%q", tc.name, out)
			}
			if out != "" {
				t.Errorf("denied helper emitted stdout = %q, want empty", out)
			}
		})
	}

	for _, action := range []string{"store", "erase"} {
		t.Run("ignores/"+action, func(t *testing.T) {
			out, err := runShellHelper(t, env, helper, action, "protocol=https\nhost=evil.example\npath=other/repo\n\n")
			if err != nil {
				t.Fatalf("helper %s failed: %v", action, err)
			}
			if out != "" {
				t.Errorf("helper %s emitted stdout = %q, want empty", action, out)
			}
		})
	}
}

func TestGitCredentialFillResetsGlobalHelpersAndScopesCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}

	const (
		token        = "ghs_scoped_token_98765"
		globalSecret = "leaked-global-secret"
		repository   = "owainlewis/machinist"
	)

	env, err := GitHubEnvironment(token, repository)
	if err != nil {
		t.Fatalf("GitHubEnvironment error: %v", err)
	}

	// Poison global Git config with fallback credential helpers for both default
	// and github.com scopes, plus an Authorization extraHeader, to verify that
	// GitHubEnvironment resets all of them.
	homeDir := t.TempDir()
	globalConfig := filepath.Join(homeDir, "gitconfig")
	poisonedConfig := `[credential]
	helper = !printf 'username=leaked\npassword=` + globalSecret + `\n'
[credential "https://github.com"]
	helper = !printf 'username=leaked\npassword=` + globalSecret + `\n'
[http]
	extraHeader = Authorization: Bearer ` + globalSecret + `
[http "https://github.com/"]
	extraHeader = Authorization: Bearer ` + globalSecret + `
`
	if err := os.WriteFile(globalConfig, []byte(poisonedConfig), 0o600); err != nil {
		t.Fatalf("write global git config: %v", err)
	}

	workDir := t.TempDir()
	runGit := func(stdin string, args ...string) (string, string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		cmd.Env = append([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + homeDir,
			"GIT_CONFIG_GLOBAL=" + globalConfig,
			"GIT_CONFIG_NOSYSTEM=1",
		}, env...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	// Verify matching repository succeeds with and without .git suffix, both via
	// explicit fields and via url= (which requires credential.useHttpPath=true).
	allowedInputs := []struct {
		name  string
		stdin string
	}{
		{
			name:  repository,
			stdin: "protocol=https\nhost=github.com\npath=" + repository + "\n\n",
		},
		{
			name:  repository + ".git",
			stdin: "protocol=https\nhost=github.com\npath=" + repository + ".git\n\n",
		},
		{
			name:  "url " + repository,
			stdin: "url=https://github.com/" + repository + "\n\n",
		},
		{
			name:  "url " + repository + ".git",
			stdin: "url=https://github.com/" + repository + ".git\n\n",
		},
	}
	for _, tc := range allowedInputs {
		t.Run("fill allows "+tc.name, func(t *testing.T) {
			stdout, stderr, err := runGit(tc.stdin, "credential", "fill")
			if err != nil {
				t.Fatalf("git credential fill failed: %v (stderr=%q)", err, stderr)
			}
			if !strings.Contains(stdout, "username=x-access-token\n") {
				t.Errorf("stdout missing expected username: %q", stdout)
			}
			if !strings.Contains(stdout, "password="+token+"\n") {
				t.Errorf("stdout missing expected token: %q", stdout)
			}
			if strings.Contains(stdout, globalSecret) {
				t.Errorf("stdout leaked global credential: %q", stdout)
			}
		})
	}

	// Verify non-matching targets fail and do NOT fall back to the poisoned global helpers.
	deniedTargets := []struct {
		name  string
		stdin string
	}{
		{
			name:  "other repository on github.com",
			stdin: "protocol=https\nhost=github.com\npath=owainlewis/other-repo\n\n",
		},
		{
			name:  "other host",
			stdin: "protocol=https\nhost=example.com\npath=owainlewis/machinist\n\n",
		},
		{
			name:  "insecure http protocol",
			stdin: "protocol=http\nhost=github.com\npath=owainlewis/machinist\n\n",
		},
	}
	for _, tc := range deniedTargets {
		t.Run("fill denies "+tc.name, func(t *testing.T) {
			stdout, _, err := runGit(tc.stdin, "credential", "fill")
			if err == nil {
				t.Fatalf("expected git credential fill to fail for %s, got stdout=%q", tc.name, stdout)
			}
			if strings.Contains(stdout, token) {
				t.Errorf("stdout leaked scoped token: %q", stdout)
			}
			if strings.Contains(stdout, globalSecret) {
				t.Errorf("stdout fell back to global credential helper: %q", stdout)
			}
		})
	}

	// Verify Git sees the URL insteadOf rewrites and empty extraHeader resets.
	stdout, stderr, err := runGit("", "config", "--get-all", "url.https://github.com/.insteadOf")
	if err != nil {
		t.Fatalf("git config url insteadOf failed: %v (stderr=%q)", err, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	wantInsteadOf := []string{"git@github.com:", "ssh://git@github.com/"}
	if !slices.Equal(lines, wantInsteadOf) {
		t.Errorf("url.https://github.com/.insteadOf = %v, want %v", lines, wantInsteadOf)
	}
}
