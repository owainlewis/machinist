package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

type fakeGitHubOAuth struct {
	mu           sync.Mutex
	server       *httptest.Server
	expectedPKCE string
	user         map[string]any
	emails       []map[string]any
	lastVerifier string
}

func newFakeGitHubOAuth(t *testing.T) *fakeGitHubOAuth {
	t.Helper()
	fg := &fakeGitHubOAuth{}
	fg.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			fg.mu.Lock()
			verifier := r.FormValue("code_verifier")
			fg.lastVerifier = verifier
			expected := fg.expectedPKCE
			fg.mu.Unlock()
			if expected != "" && pkceS256Challenge(verifier) != expected {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_super_secret_ephemeral_token"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer gho_super_secret_ephemeral_token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fg.mu.Lock()
			u := fg.user
			fg.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(u)
		case "/user/emails":
			if r.Header.Get("Authorization") != "Bearer gho_super_secret_ephemeral_token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fg.mu.Lock()
			em := fg.emails
			fg.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(em)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fg.server.Close)
	return fg
}

func (fg *fakeGitHubOAuth) setIdentity(id int64, login, name, avatar string, emails []map[string]any) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.user = map[string]any{
		"id":         id,
		"login":      login,
		"name":       name,
		"avatar_url": avatar,
		"email":      "unverified-profile-email@evil.example",
	}
	fg.emails = emails
}

type oauthFixture struct {
	server *Server
	store  *tickets.Store
	h      http.Handler
	gh     *fakeGitHubOAuth
	logs   *bytes.Buffer
}

func newOAuthFixture(t *testing.T, bootstrapAdmin string) *oauthFixture {
	t.Helper()
	fakegh.Install(t, testConfig().GitHub.Handle)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gh := newFakeGitHubOAuth(t)
	logs := &bytes.Buffer{}
	cfg := testConfig()
	cfg.Server.PublicURL = "https://machinist.example.com"
	cfg.Auth.BootstrapAdmin = bootstrapAdmin
	cfg.Auth.GitHub.Enabled = true
	cfg.Auth.GitHub.ClientID = "gh-client-id"
	cfg.Auth.GitHub.ClientSecret = "gh-client-secret"

	srv, err := New(cfg, store, testFactory(), "v1.2.3", logs)
	if err != nil {
		t.Fatal(err)
	}
	srv.githubOAuthBase = gh.server.URL
	srv.githubAPIBase = gh.server.URL
	srv.oauthHTTPClient = gh.server.Client()
	srv.authLimiter = newIPRateLimiter(1000, 1000, 1000, nil)

	return &oauthFixture{server: srv, store: store, h: srv.Handler(), gh: gh, logs: logs}
}

func (of *oauthFixture) doOAuthLogin(t *testing.T, returnTo string, preCookie *http.Cookie) (*http.Response, *http.Cookie) {
	t.Helper()
	startPath := "/auth/github/start"
	if returnTo != "" {
		startPath += "?return_to=" + url.QueryEscape(returnTo)
	}
	startReq := httptest.NewRequest(http.MethodGet, startPath, nil)
	startReq.Host = "machinist.example.com"
	if preCookie != nil {
		startReq.AddCookie(preCookie)
	}
	startRec := httptest.NewRecorder()
	of.h.ServeHTTP(startRec, startReq)
	startResp := startRec.Result()
	if startResp.StatusCode != http.StatusFound {
		t.Fatalf("start status = %d: %s", startResp.StatusCode, startRec.Body.String())
	}
	loc, err := url.Parse(startResp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	challenge := loc.Query().Get("code_challenge")
	if state == "" || challenge == "" || loc.Query().Get("code_challenge_method") != "S256" || loc.Query().Get("scope") != "read:user user:email" {
		t.Fatalf("authorize URL = %s", loc)
	}
	of.gh.mu.Lock()
	of.gh.expectedPKCE = challenge
	of.gh.mu.Unlock()

	var oauthC *http.Cookie
	for _, c := range startResp.Cookies() {
		if c.Name == secureOAuthCookie {
			oauthC = c
			break
		}
	}
	if oauthC == nil || !oauthC.Secure || !oauthC.HttpOnly {
		t.Fatalf("missing secure __Host- oauth cookie: %v", startResp.Cookies())
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=code123&state="+url.QueryEscape(state), nil)
	cbReq.Host = "machinist.example.com"
	cbReq.AddCookie(oauthC)
	cbRec := httptest.NewRecorder()
	of.h.ServeHTTP(cbRec, cbReq)
	cbResp := cbRec.Result()
	return cbResp, findCookie(cbResp)
}

func TestFailClosedPublicStartupValidation(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 1. Non-loopback public_url without auth.github.enabled fails closed.
	cfg := testConfig()
	cfg.Server.PublicURL = "https://machinist.example.com"
	if _, err := New(cfg, store, testFactory(), "v1", io.Discard); err == nil || !strings.Contains(err.Error(), "allow_token_only_public") {
		t.Fatalf("expected fail-closed startup error, got %v", err)
	}

	// 2. Explicit allow_token_only_public: true allows starting.
	cfg.Auth.AllowTokenOnlyPublic = true
	if _, err := New(cfg, store, testFactory(), "v1", io.Discard); err != nil {
		t.Fatalf("explicit allow_token_only_public failed: %v", err)
	}

	// 3. auth.github.enabled requires public_url and OAuth env secrets.
	cfg = testConfig()
	cfg.Auth.GitHub.Enabled = true
	if _, err := New(cfg, store, testFactory(), "v1", io.Discard); err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("expected public_url error, got %v", err)
	}
	cfg.Server.PublicURL = "https://machinist.example.com"
	if _, err := New(cfg, store, testFactory(), "v1", io.Discard); err == nil || !strings.Contains(err.Error(), "GITHUB_OAUTH_CLIENT_ID") {
		t.Fatalf("expected OAuth credentials error, got %v", err)
	}
}

func TestGitHubOAuthBootstrapInvitesRenameAndSecurity(t *testing.T) {
	of := newOAuthFixture(t, "github:owainlewis")

	// Unauthenticated /api/v1/auth/status leaks nothing and shows GitHub only.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	req.Host = "machinist.example.com"
	rec := httptest.NewRecorder()
	of.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authenticated":false`) || !strings.Contains(rec.Body.String(), `"methods":["github"]`) || !strings.Contains(rec.Body.String(), `"token_signin_enabled":false`) {
		t.Fatalf("auth/status = %d: %s", rec.Code, rec.Body.String())
	}

	// 1. Bootstrap admin signs in once, gets admin role + session cookie + audit JSON.
	of.gh.setIdentity(101, "OwainLewis", "Owain Lewis", "https://avatars.githubusercontent.com/u/101", []map[string]any{
		{"email": "owain@example.com", "primary": true, "verified": true},
	})
	resp, adminCookie := of.doOAuthLogin(t, "/#/jobs", nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/#/jobs" || adminCookie == nil || adminCookie.Name != secureSessionCookie {
		t.Fatalf("bootstrap login = %d loc=%q cookie=%+v", resp.StatusCode, resp.Header.Get("Location"), adminCookie)
	}
	// GitHub ephemeral token must never be logged.
	if strings.Contains(of.logs.String(), "gho_super_secret") {
		t.Fatalf("ephemeral GitHub access token leaked to logs: %s", of.logs.String())
	}
	if !strings.Contains(of.logs.String(), `"action":"member.bootstrapped"`) || !strings.Contains(of.logs.String(), `"action":"auth.login.succeeded"`) {
		t.Fatalf("missing bootstrap audit logs: %s", of.logs.String())
	}

	// GET /api/v1/me returns user profile and role.
	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meReq.Host = "machinist.example.com"
	meReq.AddCookie(adminCookie)
	meRec := httptest.NewRecorder()
	of.h.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK || !strings.Contains(meRec.Body.String(), `"github_login":"OwainLewis"`) || !strings.Contains(meRec.Body.String(), `"role":"admin"`) {
		t.Fatalf("GET /api/v1/me = %d: %s", meRec.Code, meRec.Body.String())
	}

	// 2. Regenerating/revoking a workspace token with the SAME name as the OAuth admin
	// must NEVER clobber the admin's OAuth session cookie!
	if _, err := of.store.CreateToken(t.Context(), "OwainLewis", tickets.RoleOperator); err != nil {
		t.Fatal(err)
	}
	regenReq := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/OwainLewis/regenerate", nil)
	regenReq.Host = "machinist.example.com"
	regenReq.AddCookie(adminCookie)
	regenRec := httptest.NewRecorder()
	of.h.ServeHTTP(regenRec, regenReq)
	if regenRec.Code != http.StatusOK || findCookie(regenRec.Result()) != nil {
		t.Fatalf("OAuth admin regenerateToken touched cookie: status=%d cookies=%v", regenRec.Code, regenRec.Result().Cookies())
	}
	revReq := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/OwainLewis", nil)
	revReq.Host = "machinist.example.com"
	revReq.AddCookie(adminCookie)
	revRec := httptest.NewRecorder()
	of.h.ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusOK || findCookie(revRec.Result()) != nil {
		t.Fatalf("OAuth admin revokeToken touched cookie: status=%d cookies=%v", revRec.Code, revRec.Result().Cookies())
	}

	// 3. Handle rename safety: admin renames handle to "owain-new", and an attacker
	// registers "owainlewis" with a different numeric github_id (999).
	// Admin still logs in via github_id=101 (handle refreshed), attacker is denied (bootstrap never re-arms!).
	of.gh.setIdentity(101, "owain-new", "Owain Lewis", "https://avatars.githubusercontent.com/u/101", []map[string]any{
		{"email": "owain@example.com", "primary": true, "verified": true},
	})
	resp, adminCookie2 := of.doOAuthLogin(t, "//evil.example/steal", nil)
	// Open redirect return_to="//evil.example/steal" must sanitize to "/".
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" || adminCookie2 == nil {
		t.Fatalf("renamed admin login / open redirect = %d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	of.gh.setIdentity(999, "owainlewis", "Imposter", "https://avatars.githubusercontent.com/u/999", []map[string]any{
		{"email": "imposter@example.com", "primary": true, "verified": true},
	})
	resp, imposterCookie := of.doOAuthLogin(t, "/", nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/#/sign-in?error=not_invited" || imposterCookie != nil {
		t.Fatalf("imposter login = %d loc=%q cookie=%+v", resp.StatusCode, resp.Header.Get("Location"), imposterCookie)
	}
	if !strings.Contains(of.logs.String(), `"action":"auth.login.denied"`) {
		t.Fatalf("missing denied audit log: %s", of.logs.String())
	}

	// 4. Targeted invite via email (verified only) + fragment prepare flow.
	_, secret, err := of.store.CreateInvite(t.Context(), tickets.UserRoleMember, "", "alice@team.example", "cli")
	if err != nil {
		t.Fatal(err)
	}
	prepReq := httptest.NewRequest(http.MethodPost, "/api/v1/invites/prepare", bytes.NewBufferString(`{"token":"`+secret+`"}`))
	prepReq.Host = "machinist.example.com"
	prepReq.Header.Set("Content-Type", "application/json")
	prepRec := httptest.NewRecorder()
	of.h.ServeHTTP(prepRec, prepReq)
	if prepRec.Code != http.StatusOK || !strings.Contains(prepRec.Body.String(), `"role":"member"`) {
		t.Fatalf("prepare invite = %d: %s", prepRec.Code, prepRec.Body.String())
	}
	var prepCookie *http.Cookie
	for _, c := range prepRec.Result().Cookies() {
		if c.Name == secureOAuthCookie {
			prepCookie = c
		}
	}
	if prepCookie == nil {
		t.Fatal("prepare invite did not set oauth cookie")
	}

	// Wrong user (unverified email only) trying to redeem alice's invite is rejected!
	of.gh.setIdentity(202, "bob", "Bob", "https://avatars.githubusercontent.com/u/202", []map[string]any{
		{"email": "alice@team.example", "primary": true, "verified": false},
	})
	resp, bobCookie := of.doOAuthLogin(t, "/", prepCookie)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/#/sign-in?error=not_invited" || bobCookie != nil {
		t.Fatalf("unverified email redeem = %d loc=%q cookie=%+v", resp.StatusCode, resp.Header.Get("Location"), bobCookie)
	}

	// Real Alice with verified email redeems invite automatically on direct GitHub login.
	of.gh.setIdentity(203, "alice", "Alice", "https://avatars.githubusercontent.com/u/203", []map[string]any{
		{"email": "alice@team.example", "primary": true, "verified": true},
	})
	resp, aliceCookie := of.doOAuthLogin(t, "/", nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" || aliceCookie == nil {
		t.Fatalf("alice invite login = %d loc=%q cookie=%+v", resp.StatusCode, resp.Header.Get("Location"), aliceCookie)
	}

	// 5. /api/v1/me/sessions: admin lists 2 active sessions, revokes 1, cannot revoke alice's session.
	sessReq := httptest.NewRequest(http.MethodGet, "/api/v1/me/sessions", nil)
	sessReq.Host = "machinist.example.com"
	sessReq.AddCookie(adminCookie2)
	sessRec := httptest.NewRecorder()
	of.h.ServeHTTP(sessRec, sessReq)
	var sessList struct {
		Sessions []sessionView `json:"sessions"`
	}
	if err := json.Unmarshal(sessRec.Body.Bytes(), &sessList); err != nil || sessRec.Code != http.StatusOK || len(sessList.Sessions) != 2 {
		t.Fatalf("GET /api/v1/me/sessions = %d: %s", sessRec.Code, sessRec.Body.String())
	}
	// Alice cannot revoke admin's session (404).
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+sessList.Sessions[0].ID, nil)
	delReq.Host = "machinist.example.com"
	delReq.AddCookie(aliceCookie)
	delRec := httptest.NewRecorder()
	of.h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user session revoke = %d, want 404", delRec.Code)
	}
	// Admin revokes their other session -> 204.
	var otherID string
	for _, s := range sessList.Sessions {
		if !s.Current {
			otherID = s.ID
		}
	}
	delReq = httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+otherID, nil)
	delReq.Host = "machinist.example.com"
	delReq.AddCookie(adminCookie2)
	delRec = httptest.NewRecorder()
	of.h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("own session revoke = %d, want 204", delRec.Code)
	}
	// Revoked adminCookie no longer authenticates.
	checkReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	checkReq.Host = "machinist.example.com"
	checkReq.AddCookie(adminCookie)
	checkRec := httptest.NewRecorder()
	of.h.ServeHTTP(checkRec, checkReq)
	if checkRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session still worked = %d, want 401", checkRec.Code)
	}
}

func TestOAuthCallbackRejectsReplayAndStateMismatch(t *testing.T) {
	of := newOAuthFixture(t, "github:owainlewis")
	of.gh.setIdentity(101, "owainlewis", "Owain", "https://avatars.githubusercontent.com/u/101", nil)

	startReq := httptest.NewRequest(http.MethodGet, "/auth/github/start", nil)
	startReq.Host = "machinist.example.com"
	startRec := httptest.NewRecorder()
	of.h.ServeHTTP(startRec, startReq)
	loc, _ := url.Parse(startRec.Header().Get("Location"))
	state := loc.Query().Get("state")
	var oauthC *http.Cookie
	for _, c := range startRec.Result().Cookies() {
		if c.Name == secureOAuthCookie {
			oauthC = c
		}
	}

	// Wrong state fails AND consumes the transaction (so subsequent replay with right state ALSO fails!).
	badReq := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c1&state=wrong-state", nil)
	badReq.Host = "machinist.example.com"
	badReq.AddCookie(oauthC)
	badRec := httptest.NewRecorder()
	of.h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusFound || badRec.Header().Get("Location") != "/#/sign-in?error=oauth_failed" {
		t.Fatalf("wrong state = %d loc=%q", badRec.Code, badRec.Header().Get("Location"))
	}

	// Replaying with the original state must also fail because the transaction was consumed.
	replayReq := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c1&state="+url.QueryEscape(state), nil)
	replayReq.Host = "machinist.example.com"
	replayReq.AddCookie(oauthC)
	replayRec := httptest.NewRecorder()
	of.h.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusFound || replayRec.Header().Get("Location") != "/#/sign-in?error=oauth_failed" {
		t.Fatalf("replay = %d loc=%q", replayRec.Code, replayRec.Header().Get("Location"))
	}
}
