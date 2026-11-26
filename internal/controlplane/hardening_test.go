package controlplane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestProxyAwareHostCookieAndSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	opToken := f.newToken(t, "alice", tickets.RoleOperator)
	f.server.config.Server.PublicURL = "https://machinist.example.com"

	// Over a plain-HTTP backend connection (like Cloud Run behind TLS termination),
	// https:// public_url still produces __Host-machinist_session with Secure=true and HSTS.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewBufferString(`{"token":"`+opToken+`"}`))
	req.Host = "machinist.example.com"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	cookie := findCookie(resp)
	if cookie == nil || cookie.Name != secureSessionCookie || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie = %+v, want __Host- Secure HttpOnly Lax Path=/", cookie)
	}
	if !strings.HasPrefix(cookie.Value, tickets.SessionPrefix) {
		t.Fatalf("cookie value = %q, want mses_ prefix", cookie.Value)
	}
	if got := resp.Header.Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=31536000") {
		t.Fatalf("HSTS = %q", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"base-uri 'none'", "frame-ancestors 'none'", "https://avatars.githubusercontent.com", "blob:"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "form-action") {
		t.Fatalf("CSP should not contain form-action: %s", csp)
	}
}

func TestHealthzOmitsVersionOnPublicNonLoopback(t *testing.T) {
	f := newFixture(t)
	f.server.config.Server.PublicURL = "https://machinist.example.com"

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "machinist.example.com"
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "v1.2.3") || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("public healthz = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCanonicalHostAndLoopbackProxyHost(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store := openTestStore(t)

	// 1. Loopback listener behind local Caddy/nginx proxy: allows BOTH loopback and public_url host.
	cfg := testConfig()
	cfg.Server.Listen = "127.0.0.1:7331"
	cfg.Server.PublicURL = "https://machinist.example.com"
	cfg.Auth.AllowTokenOnlyPublic = true
	srv, err := New(cfg, store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	if rec := serveWithHost(h, "/", "machinist.example.com"); rec.Code != http.StatusOK {
		t.Fatalf("public_url host on loopback proxy = %d, want 200", rec.Code)
	}
	if rec := serveWithHost(h, "/healthz", "127.0.0.1:7331"); rec.Code != http.StatusOK {
		t.Fatalf("loopback host on loopback proxy = %d, want 200", rec.Code)
	}
	if rec := serveWithHost(h, "/", "evil.example"); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("evil host on loopback proxy = %d, want 421", rec.Code)
	}

	// 2. Non-loopback listener (Cloud Run) with public_url: redirects *.run.app on GET /,
	// rejects *.run.app on /api/v1/* and /auth/github/callback, exempts /healthz.
	cfg.Server.Listen = "0.0.0.0:8080"
	srv2, err := New(cfg, store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h2 := srv2.Handler()
	if rec := serveWithHost(h2, "/healthz", "machinist-abc.a.run.app"); rec.Code != http.StatusOK {
		t.Fatalf("healthz on run.app alias = %d, want 200", rec.Code)
	}
	rec := serveWithHost(h2, "/?foo=bar", "machinist-abc.a.run.app")
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://machinist.example.com/?foo=bar" {
		t.Fatalf("GET / on run.app = %d, Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serveWithHost(h2, "/api/v1/tickets", "machinist-abc.a.run.app"); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("API on run.app = %d, want 421", rec.Code)
	}
	if rec := serveWithHost(h2, "/auth/github/callback", "machinist-abc.a.run.app"); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("OAuth callback on run.app = %d, want 421", rec.Code)
	}
	// Local loopback access (e.g. machinist reload on 127.0.0.1:8080) still works on wildcard listeners.
	if rec := serveWithHost(h2, "/api/v1/tickets", "127.0.0.1:8080"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("loopback API on wildcard listener = %d, want 401 (reached auth, not 421)", rec.Code)
	}
}

func TestCSRFProtectionBlocksCrossOriginAndAllowsBearer(t *testing.T) {
	f := newFixture(t)
	cookie := f.signInCookie(t, f.token)

	// 1. Sec-Fetch-Site: cross-site on cookie mutation -> 403.
	req, _ := http.NewRequest(http.MethodDelete, f.http.URL+"/api/v1/session", nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site cookie mutation = %d, want 403", resp.StatusCode)
	}

	// 2. Origin: null on POST /api/v1/session (Login CSRF) -> 403.
	req, _ = http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/session", bytes.NewBufferString(`{"token":"`+f.token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "null")
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin: null login = %d, want 403", resp.StatusCode)
	}

	// 3. Cross-origin Origin header without Sec-Fetch-Site -> 403.
	req, _ = http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/tokens", bytes.NewBufferString(`{"name":"x","role":"operator"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin cookie POST = %d, want 403", resp.StatusCode)
	}

	// 4. Form Content-Type on cookie JSON mutation -> 415.
	req, _ = http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/tokens", bytes.NewBufferString(`{"name":"x","role":"operator"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "text/plain")
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain cookie POST = %d, want 415", resp.StatusCode)
	}

	// 5. Authorization: Bearer bypasses CSRF, and an invalid Bearer does NOT fall back to cookie.
	req, _ = http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/tokens", bytes.NewBufferString(`{"name":"ci-bot","role":"operator"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Origin", "https://evil.example")
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid Bearer with cross-origin = %d, want 201", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, f.http.URL+"/api/v1/tickets", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	req.AddCookie(cookie)
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid Bearer with valid cookie = %d, want 401 (no cookie fallback)", resp.StatusCode)
	}
}

func TestRunnerRoutesIgnoreSessionCookies(t *testing.T) {
	f := newFixture(t)
	cookie := f.signInCookie(t, f.token)

	for _, path := range []string{
		"/api/v1/runners/poll",
		"/api/v1/runs/run_1/heartbeat",
		"/api/v1/runs/run_1/events",
		"/api/v1/runs/run_1/complete",
		"/api/v1/runs/run_1/github-token",
	} {
		req, _ := http.NewRequest(http.MethodPost, f.http.URL+path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := f.http.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie-only POST %s = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestRoleAllRejectedOnPublicDeploymentsWithWarning(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store := openTestStore(t)

	legacyToken, err := store.CreateToken(t.Context(), "legacy", tickets.RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	opToken, err := store.CreateToken(t.Context(), "alice", tickets.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	runToken, err := store.CreateToken(t.Context(), "vm-1", tickets.RoleRunner)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	cfg := testConfig()
	cfg.Server.PublicURL = "https://machinist.example.com"
	cfg.Auth.AllowTokenOnlyPublic = true
	srv, err := New(cfg, store, testFactory(), "v1.2.3", &logs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "legacy role-less token") {
		t.Fatalf("expected startup warning for RoleAll token, got logs: %q", logs.String())
	}

	h := srv.Handler()
	call := func(method, path, bearer string, body string) int {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Host = "machinist.example.com"
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call(http.MethodGet, "/api/v1/tickets", legacyToken, ""); code != http.StatusForbidden {
		t.Fatalf("legacy operator route = %d, want 403", code)
	}
	if code := call(http.MethodPost, "/api/v1/runners/poll", legacyToken, `{}`); code != http.StatusForbidden {
		t.Fatalf("legacy runner route = %d, want 403", code)
	}
	if code := call(http.MethodPost, "/mcp", legacyToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != http.StatusForbidden {
		t.Fatalf("legacy MCP route = %d, want 403", code)
	}
	if code := call(http.MethodPost, "/api/v1/session", "", `{"token":"`+legacyToken+`"}`); code != http.StatusForbidden {
		t.Fatalf("legacy signIn = %d, want 403", code)
	}
	// Typed tokens continue to work cleanly.
	if code := call(http.MethodGet, "/api/v1/tickets", opToken, ""); code != http.StatusOK {
		t.Fatalf("typed operator = %d, want 200", code)
	}
	if code := call(http.MethodPost, "/api/v1/runners/poll", runToken, `{}`); code != http.StatusNoContent {
		t.Fatalf("typed runner = %d, want 204", code)
	}
}

func TestDisableTokenSigninBlocksSessionPost(t *testing.T) {
	f := newFixture(t)
	disabled := true
	f.server.config.Auth.DisableTokenSignin = &disabled
	resp, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": f.token})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled token sign-in = %d, want 403", resp.StatusCode)
	}
}

func TestSignOutAllowedForReadScopedToken(t *testing.T) {
	f := newFixture(t)
	readToken := f.newScopedToken(t, "viewer-tok", tickets.ScopeRead)
	resp, _ := f.call(t, http.MethodDelete, "/api/v1/session", readToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("read-scoped sign-out = %d, want 204", resp.StatusCode)
	}
}

func TestRateLimiterAndClientIPExtraction(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	req.RemoteAddr = "198.51.100.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1, 10.0.0.2")

	if got := clientIP(req, 0); got != "198.51.100.1" {
		t.Fatalf("hops=0 got %q, want 198.51.100.1", got)
	}
	if got := clientIP(req, 1); got != "10.0.0.2" {
		t.Fatalf("hops=1 got %q, want 10.0.0.2", got)
	}
	if got := clientIP(req, 2); got != "10.0.0.1" {
		t.Fatalf("hops=2 got %q, want 10.0.0.1", got)
	}
	if got := clientIP(req, 3); got != "203.0.113.9" {
		t.Fatalf("hops=3 got %q, want 203.0.113.9", got)
	}

	// Bounded LRU eviction & burst enforcement.
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	limiter := newIPRateLimiter(2, authRateLimitPerSec, 2, func() time.Time { return now })
	if !limiter.Allow("1.1.1.1") {
		t.Fatal("1st request within burst should pass")
	}
	if !limiter.Allow("1.1.1.1") {
		t.Fatal("2nd request within burst should pass")
	}
	if limiter.Allow("1.1.1.1") {
		t.Fatal("3rd request exceeding burst should be rejected")
	}
	if !limiter.Allow("2.2.2.2") || !limiter.Allow("3.3.3.3") {
		t.Fatal("new IPs should pass")
	}
	if len(limiter.entries) != 2 {
		t.Fatalf("entries = %d, want capped at 2", len(limiter.entries))
	}

	// End-to-end 429 on POST /api/v1/session after burst (10).
	f := newFixture(t)
	for i := 0; i < int(authRateLimitBurst); i++ {
		resp, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": "wrong"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, resp.StatusCode)
		}
	}
	resp, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": "wrong"})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limited response = %d, Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestReloadTreatsPublicURLProxyHopsAndAuthAsRestartOnly(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	dir := t.TempDir()
	factoryDir := filepath.Join(dir, "factory")
	if err := os.MkdirAll(filepath.Join(factoryDir, "presets"), 0o755); err != nil {
		t.Fatal(err)
	}
	presetYAML := "description: test preset\nsteps:\n  - name: work\n    run: \"true\"\n"
	if err := os.WriteFile(filepath.Join(factoryDir, "presets", "default.yaml"), []byte(presetYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	initial := "server:\n  listen: 127.0.0.1:7331\n  factory: factory\n  public_url: http://127.0.0.1:7331\n  trusted_proxy_hops: 0\ngithub:\n  handle: machinist-bot\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	loadedCfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(filepath.Join(dir, "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv, err := New(loadedCfg, store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate public_url, trusted_proxy_hops, and auth in config.yaml and reload.
	updated := "server:\n  listen: 127.0.0.1:7331\n  factory: factory\n  public_url: https://machinist.example.com\n  trusted_proxy_hops: 1\nauth:\n  disable_token_signin: true\n  github:\n    enabled: true\ngithub:\n  handle: machinist-bot\n"
	if err := os.WriteFile(cfgPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := srv.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	joined := strings.Join(res.RestartNeeded, ",")
	for _, want := range []string{"server.public_url", "server.trusted_proxy_hops", "auth"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RestartNeeded = %v, missing %q", res.RestartNeeded, want)
		}
	}
	effective := srv.state().config
	if effective.Server.PublicURL != "http://127.0.0.1:7331" || effective.Server.TrustedProxyHops != 0 || effective.Auth.GitHub.Enabled || !effective.Auth.TokenSigninEnabled() {
		t.Fatalf("restart-only auth/proxy settings leaked into effective config: %+v", effective)
	}
}

func TestRunnerPollStillRequiresTypedTokenWithBearer(t *testing.T) {
	f := newFixture(t)
	runnerTok := f.newToken(t, "vm-2", tickets.RoleRunner)
	resp, _ := f.call(t, http.MethodPost, "/api/v1/runners/poll", runnerTok, protocol.PollRequest{})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("runner poll = %d, want 204", resp.StatusCode)
	}
}

func TestCloudRunRefusesSQLiteDatabase(t *testing.T) {
	t.Setenv("K_SERVICE", "machinist-prod")
	cfg := testConfig()
	cfg.Server.Database = filepath.Join(t.TempDir(), "tickets.db")
	if _, err := New(cfg, nil, testFactory(), "v1.0.0", io.Discard); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected Cloud Run SQLite guardrail error, got: %v", err)
	}
	// With PostgreSQL URL but empty public_url and allow_token_only_public=false, refuse startup.
	cfg.Server.Database = "postgres://machinist:secret@db.example.com:5432/machinist?sslmode=require"
	if _, err := New(cfg, nil, testFactory(), "v1.0.0", io.Discard); err == nil || !strings.Contains(err.Error(), "server.public_url") {
		t.Fatalf("expected Cloud Run empty public_url guardrail error, got: %v", err)
	}
	// With Postgres + explicit allow_token_only_public, startup succeeds and redacts DSN in logs.
	cfg.Auth.AllowTokenOnlyPublic = true
	var logs bytes.Buffer
	if _, err := New(cfg, nil, testFactory(), "v1.0.0", &logs); err != nil {
		t.Fatalf("unexpected error with postgres URL: %v", err)
	}
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), "storage backend: postgres") {
		t.Fatalf("unexpected startup log: %q", logs.String())
	}
}

func TestReloadTreatsNotifyWebhookAsRestartOnly(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	dir := t.TempDir()
	factoryDir := filepath.Join(dir, "factory")
	if err := os.MkdirAll(filepath.Join(factoryDir, "presets"), 0o755); err != nil {
		t.Fatal(err)
	}
	presetYAML := "description: test preset\nsteps:\n  - name: work\n    run: \"true\"\n"
	if err := os.WriteFile(filepath.Join(factoryDir, "presets", "default.yaml"), []byte(presetYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	initial := "server:\n  listen: 127.0.0.1:7331\n  factory: factory\ngithub:\n  handle: machinist-bot\nnotify:\n  webhook: https://hooks.example.com/old-secret\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	loadedCfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	srv, err := New(loadedCfg, store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	updated := "server:\n  listen: 127.0.0.1:7331\n  factory: factory\ngithub:\n  handle: machinist-bot\nnotify:\n  webhook: https://hooks.example.com/new-secret\n"
	if err := os.WriteFile(cfgPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := srv.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	joined := strings.Join(res.RestartNeeded, ",")
	if !strings.Contains(joined, "notify.webhook") || strings.Contains(joined, "new-secret") {
		t.Fatalf("RestartNeeded = %v, want notify.webhook without URL", res.RestartNeeded)
	}
	if srv.state().config.Notify.Webhook != "https://hooks.example.com/old-secret" {
		t.Fatalf("effective Notify.Webhook changed on reload: %q", srv.state().config.Notify.Webhook)
	}
}

func TestGitHubSemBypassedOnCacheHitAndRespectsContext(t *testing.T) {
	f := newFixture(t)
	// Seed cache with an installed repo entry.
	f.server.githubCache.setInstalled("owner/repo", f.server.currentTime().Add(githubCacheTTL), f.server.githubCache.generation())

	// Saturate githubSem completely.
	for range cap(f.server.githubSem) {
		f.server.githubSem <- struct{}{}
	}
	defer func() {
		for range cap(f.server.githubSem) {
			<-f.server.githubSem
		}
	}()

	// Cache hit must succeed immediately even when githubSem is saturated.
	if err := f.server.appInstalled(t.Context(), nil, "owner/repo", false); err != nil {
		t.Fatalf("cached appInstalled blocked or failed: %v", err)
	}

	// Cache miss (refresh=true) must wait on githubSem and bail immediately on ctx cancellation.
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if err := f.server.appInstalled(ctx, nil, "owner/repo", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncached appInstalled = %v, want context.DeadlineExceeded", err)
	}
}
