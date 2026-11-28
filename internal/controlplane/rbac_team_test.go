package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

func (of *oauthFixture) createUserWithSession(t *testing.T, ghID int64, login, email, role string) (tickets.User, *http.Cookie) {
	t.Helper()
	if _, _, err := of.store.CreateInvite(t.Context(), role, login, "", "cli"); err != nil {
		t.Fatal(err)
	}
	of.gh.setIdentity(ghID, login, strings.ToUpper(login[:1])+login[1:], "https://avatars.githubusercontent.com/u/1", []map[string]any{
		{"email": email, "primary": true, "verified": true},
	})
	resp, cookie := of.doOAuthLogin(t, "/", nil)
	if resp.StatusCode != http.StatusFound || cookie == nil {
		t.Fatalf("login for %s failed: status=%d loc=%q", login, resp.StatusCode, resp.Header.Get("Location"))
	}
	user, err := of.store.FindUserByIDOrLogin(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	return user, cookie
}

func (of *oauthFixture) doReq(method, path string, cookie *http.Cookie, bearer string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = "machinist.example.com"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	of.h.ServeHTTP(rec, req)
	return rec
}

func TestRBACMatrixAcrossAllRolesAndPlanes(t *testing.T) {
	of := newOAuthFixture(t, "github:boss")
	if err := of.store.AddProject(t.Context(), "owner/repo", "boss"); err != nil {
		t.Fatal(err)
	}

	// Bootstrap admin, invite member & viewer, create typed runner + scoped operator tokens.
	of.gh.setIdentity(1, "boss", "Boss", "https://avatars.githubusercontent.com/u/1", []map[string]any{{"email": "boss@team.example", "primary": true, "verified": true}})
	_, adminCookie := of.doOAuthLogin(t, "/", nil)
	_, memberCookie := of.createUserWithSession(t, 2, "dev", "dev@team.example", tickets.UserRoleMember)
	_, viewerCookie := of.createUserWithSession(t, 3, "obs", "obs@team.example", tickets.UserRoleViewer)

	runnerTok, err := of.store.CreateToken(t.Context(), "vm-runner", tickets.RoleRunner)
	if err != nil {
		t.Fatal(err)
	}
	readTok, err := of.store.CreateScopedToken(t.Context(), "ci-read", tickets.RoleOperator, tickets.ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	runTok, err := of.store.CreateScopedToken(t.Context(), "ci-run", tickets.RoleOperator, tickets.ScopeRun)
	if err != nil {
		t.Fatal(err)
	}

	type expectation struct {
		name   string
		method string
		path   string
		body   any
		unauth int
		viewer int
		member int
		admin  int
		runner int
	}

	matrix := []expectation{
		{"GET /api/v1/me", http.MethodGet, "/api/v1/me", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/tickets", http.MethodGet, "/api/v1/tickets", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/runners", http.MethodGet, "/api/v1/runners", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/repositories", http.MethodGet, "/api/v1/repositories", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/presets", http.MethodGet, "/api/v1/presets", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/agents", http.MethodGet, "/api/v1/agents", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/automations", http.MethodGet, "/api/v1/automations", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/ledger", http.MethodGet, "/api/v1/ledger", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/usage", http.MethodGet, "/api/v1/usage", nil, 401, 200, 200, 200, 403},
		{"GET /api/v1/stats", http.MethodGet, "/api/v1/stats", nil, 401, 200, 200, 200, 403},
		{"GET /machinist/jobs", http.MethodGet, "/machinist/jobs", nil, 401, 200, 200, 200, 403},
		{"POST /machinist/run", http.MethodPost, "/machinist/run", map[string]any{"key": "k1", "preset": "default", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "hi"}, 401, 403, 201, 200, 403},
		{"GET /api/v1/setup", http.MethodGet, "/api/v1/setup", nil, 401, 403, 403, 200, 403},
		{"GET /api/v1/settings/github", http.MethodGet, "/api/v1/settings/github", nil, 401, 403, 403, 200, 403},
		{"GET /api/v1/settings/linear", http.MethodGet, "/api/v1/settings/linear", nil, 401, 403, 403, 200, 403},
		{"GET /api/v1/tokens", http.MethodGet, "/api/v1/tokens", nil, 401, 403, 403, 200, 403},
		{"GET /api/v1/team/members", http.MethodGet, "/api/v1/team/members", nil, 401, 403, 403, 200, 403},
		{"GET /api/v1/team/invites", http.MethodGet, "/api/v1/team/invites", nil, 401, 403, 403, 200, 403},
		{"DELETE /api/v1/runners/missing", http.MethodDelete, "/api/v1/runners/missing", nil, 401, 403, 403, 404, 403},
		{"DELETE /api/v1/tickets/t_missing", http.MethodDelete, "/api/v1/tickets/t_missing", nil, 401, 403, 403, 404, 403},
		{"DELETE /machinist/jobs/t_missing", http.MethodDelete, "/machinist/jobs/t_missing", nil, 401, 403, 403, 404, 403},
		{"POST /api/v1/runners/poll", http.MethodPost, "/api/v1/runners/poll", map[string]any{}, 401, 401, 401, 401, 204},
	}

	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			if got := of.doReq(tc.method, tc.path, nil, "", tc.body).Code; got != tc.unauth {
				t.Errorf("unauth = %d, want %d", got, tc.unauth)
			}
			if got := of.doReq(tc.method, tc.path, viewerCookie, "", tc.body).Code; got != tc.viewer {
				t.Errorf("viewer = %d, want %d", got, tc.viewer)
			}
			if got := of.doReq(tc.method, tc.path, memberCookie, "", tc.body).Code; got != tc.member {
				t.Errorf("member = %d, want %d", got, tc.member)
			}
			if got := of.doReq(tc.method, tc.path, adminCookie, "", tc.body).Code; got != tc.admin {
				t.Errorf("admin = %d, want %d", got, tc.admin)
			}
			if got := of.doReq(tc.method, tc.path, nil, runnerTok, tc.body).Code; got != tc.runner {
				t.Errorf("runner = %d, want %d", got, tc.runner)
			}
		})
	}

	// MCP: cookie ignored (401); read-scoped bearer calling run_job returns HTTP 200 + JSON-RPC isError: true; run-scoped succeeds.
	mcpCall := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "run_job", "arguments": map[string]any{"repo": "owner/repo", "prompt": "do it"}},
	}
	if rec := of.doReq(http.MethodPost, "/mcp", adminCookie, "", mcpCall); rec.Code != http.StatusUnauthorized {
		t.Fatalf("MCP with cookie = %d, want 401", rec.Code)
	}
	rec := of.doReq(http.MethodPost, "/mcp", nil, readTok, mcpCall)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("MCP read token on run_job = %d: %s", rec.Code, rec.Body.String())
	}
	rec = of.doReq(http.MethodPost, "/mcp", nil, runTok, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	if rec.Code != http.StatusOK {
		t.Fatalf("MCP run token ping = %d: %s", rec.Code, rec.Body.String())
	}

	// Viewer can sign out via DELETE /api/v1/session (204).
	if rec := of.doReq(http.MethodDelete, "/api/v1/session", viewerCookie, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer DELETE /api/v1/session = %d, want 204", rec.Code)
	}
}

func TestTeamManagementLastAdminGuardOffboardingCascadeAndAudit(t *testing.T) {
	of := newOAuthFixture(t, "github:boss")
	if err := of.store.AddProject(t.Context(), "owner/repo", "boss"); err != nil {
		t.Fatal(err)
	}

	of.gh.setIdentity(1, "boss", "Boss", "https://avatars.githubusercontent.com/u/1", []map[string]any{{"email": "boss@team.example", "primary": true, "verified": true}})
	_, bossCookie := of.doOAuthLogin(t, "/", nil)
	boss, _ := of.store.FindUserByIDOrLogin(t.Context(), "boss")

	// 1. Sole active admin cannot be demoted or disabled (409 Conflict).
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+boss.ID, bossCookie, "", map[string]any{"role": "member"}); rec.Code != http.StatusConflict {
		t.Fatalf("demote sole admin = %d, want 409", rec.Code)
	}
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+boss.ID, bossCookie, "", map[string]any{"disabled": true}); rec.Code != http.StatusConflict {
		t.Fatalf("disable sole admin = %d, want 409", rec.Code)
	}

	// 2. Team invite create / regenerate / revoke + conflict on existing user.
	createRec := of.doReq(http.MethodPost, "/api/v1/team/invites", bossCookie, "", map[string]any{"role": "member", "github_login": "dev"})
	if createRec.Code != http.StatusCreated || createRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create invite = %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Invite    tickets.Invite `json:"invite"`
		InviteURL string         `json:"invite_url"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	oldSecret := strings.TrimPrefix(created.InviteURL, "https://machinist.example.com/#/invite#")
	if !strings.HasPrefix(oldSecret, tickets.InvitePrefix) {
		t.Fatalf("unexpected invite_url = %q", created.InviteURL)
	}

	// Regenerating rotates secret (old secret fails /invites/prepare, new secret succeeds).
	regenRec := of.doReq(http.MethodPost, "/api/v1/team/invites/"+created.Invite.ID+"/regenerate", bossCookie, "", nil)
	if regenRec.Code != http.StatusOK {
		t.Fatalf("regenerate invite = %d: %s", regenRec.Code, regenRec.Body.String())
	}
	var regened struct {
		InviteURL string `json:"invite_url"`
	}
	_ = json.Unmarshal(regenRec.Body.Bytes(), &regened)
	newSecret := strings.TrimPrefix(regened.InviteURL, "https://machinist.example.com/#/invite#")
	if newSecret == oldSecret {
		t.Fatal("regenerate did not rotate secret")
	}
	if rec := of.doReq(http.MethodPost, "/api/v1/invites/prepare", nil, "", map[string]string{"token": oldSecret}); rec.Code != http.StatusNotFound {
		t.Fatalf("old rotated secret = %d, want 404", rec.Code)
	}
	if rec := of.doReq(http.MethodPost, "/api/v1/invites/prepare", nil, "", map[string]string{"token": newSecret}); rec.Code != http.StatusOK {
		t.Fatalf("new rotated secret = %d, want 200", rec.Code)
	}

	// List invites never leaks secret or hash.
	listInvRec := of.doReq(http.MethodGet, "/api/v1/team/invites", bossCookie, "", nil)
	if listInvRec.Code != http.StatusOK || strings.Contains(listInvRec.Body.String(), newSecret) || strings.Contains(listInvRec.Body.String(), "token_hash") {
		t.Fatalf("list invites leaked secret or failed: %d %s", listInvRec.Code, listInvRec.Body.String())
	}

	// Dev accepts invite; immediate role downgrade takes effect on existing session.
	of.gh.setIdentity(2, "dev", "Dev", "https://avatars.githubusercontent.com/u/2", []map[string]any{{"email": "dev@team.example", "primary": true, "verified": true}})
	_, devCookie := of.doOAuthLogin(t, "/", nil)
	dev, _ := of.store.FindUserByIDOrLogin(t.Context(), "dev")

	// Creating invite for existing user returns 409 Conflict.
	if rec := of.doReq(http.MethodPost, "/api/v1/team/invites", bossCookie, "", map[string]any{"role": "member", "github_login": "dev"}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate invite for existing user = %d, want 409", rec.Code)
	}

	// Demote dev to viewer -> dev's existing cookie immediately loses POST /machinist/run.
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+dev.ID, bossCookie, "", map[string]any{"role": "viewer"}); rec.Code != http.StatusOK {
		t.Fatalf("demote dev = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := of.doReq(http.MethodPost, "/machinist/run", devCookie, "", map[string]any{"key": "k2", "preset": "default", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "hi"}); rec.Code != http.StatusForbidden {
		t.Fatalf("demoted member still ran job = %d, want 403", rec.Code)
	}

	// 3. Disabling dev (even with only 1 active admin) succeeds, revokes all active sessions, and blocks re-login.
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+dev.ID, bossCookie, "", map[string]any{"disabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("disable dev = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := of.doReq(http.MethodGet, "/api/v1/me", devCookie, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user's existing session still worked = %d, want 401", rec.Code)
	}
	resp, disabledCookie := of.doOAuthLogin(t, "/", nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/#/sign-in?error=disabled" || disabledCookie != nil {
		t.Fatalf("disabled user re-login = %d loc=%q cookie=%+v", resp.StatusCode, resp.Header.Get("Location"), disabledCookie)
	}

	// Re-enabling dev allows them to sign in again.
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+dev.ID, bossCookie, "", map[string]any{"disabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("re-enable dev = %d: %s", rec.Code, rec.Body.String())
	}
	resp, devCookie2 := of.doOAuthLogin(t, "/", nil)
	if resp.StatusCode != http.StatusFound || devCookie2 == nil {
		t.Fatalf("re-enabled user login failed: %d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 4. Concurrent last-admin guard: promote dev to admin, then concurrently try to disable BOTH boss and dev.
	if rec := of.doReq(http.MethodPatch, "/api/v1/team/members/"+dev.ID, bossCookie, "", map[string]any{"role": "admin"}); rec.Code != http.StatusOK {
		t.Fatalf("promote dev = %d", rec.Code)
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes[0] = of.doReq(http.MethodPatch, "/api/v1/team/members/"+boss.ID, devCookie2, "", map[string]any{"disabled": true}).Code
	}()
	go func() {
		defer wg.Done()
		codes[1] = of.doReq(http.MethodPatch, "/api/v1/team/members/"+dev.ID, bossCookie, "", map[string]any{"disabled": true}).Code
	}()
	wg.Wait()
	members, err := of.store.ListUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	activeAdmins := 0
	for _, m := range members {
		if m.Role == tickets.UserRoleAdmin && m.DisabledAt == nil {
			activeAdmins++
		}
	}
	if activeAdmins < 1 {
		t.Fatalf("concurrent disables left %d active admins (codes=%v)", activeAdmins, codes)
	}

	// 5. Verify structured slog JSON audit events were emitted.
	logs := of.logs.String()
	for _, action := range []string{"invite.created", "invite.regenerated", "member.role_changed", "member.disabled", "member.enabled"} {
		if !strings.Contains(logs, `"action":"`+action+`"`) {
			t.Errorf("missing audit action %q in logs", action)
		}
	}
}
