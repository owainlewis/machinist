package tickets

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func openAuthTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	store := openStore(t)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	return store, &now
}

func TestAuthSchemaAndForeignKeysEnforced(t *testing.T) {
	store, _ := openAuthTestStore(t)

	// auth_state singleton row exists with bootstrapped_at NULL.
	var id int
	var bootstrapped *string
	if err := store.q().QueryRowContext(t.Context(), `SELECT id, bootstrapped_at FROM auth_state`).Scan(&id, &bootstrapped); err != nil {
		t.Fatalf("auth_state query: %v", err)
	}
	if id != 1 || bootstrapped != nil {
		t.Fatalf("auth_state = (%d, %v), want (1, nil)", id, bootstrapped)
	}
	// Inserting a second auth_state row violates CHECK (id = 1).
	if _, err := store.q().ExecContext(t.Context(), `INSERT INTO auth_state (id, bootstrapped_at) VALUES (2, NULL)`); err == nil {
		t.Fatal("expected CHECK constraint failure for auth_state.id = 2")
	}

	// Foreign keys must reject sessions referencing a missing token_id.
	_, err := store.q().ExecContext(t.Context(), `
		INSERT INTO sessions(id,token_hash,auth_method,user_id,token_id,user_agent,ip_address,created_at,last_seen_at,idle_expires_at,expires_at)
		VALUES('ses_1','hash_1','token',NULL,'nonexistent_token','','','2026-11-26T12:00:00.000000000Z','2026-11-26T12:00:00.000000000Z','2026-12-03T12:00:00.000000000Z','2026-12-26T12:00:00.000000000Z')`)
	if err == nil {
		t.Fatal("expected foreign key constraint failure for nonexistent token_id")
	}

	// Untargeted invites must be rejected by CHECK (github_login != '' OR email != '').
	_, err = store.q().ExecContext(t.Context(), `
		INSERT INTO invites(id,role,github_login,email,token_hash,created_by,expires_at,created_at)
		VALUES('inv_1','member','','','hash_inv','cli','2026-12-03T12:00:00.000000000Z','2026-11-26T12:00:00.000000000Z')`)
	if err == nil {
		t.Fatal("expected CHECK constraint failure for untargeted invite")
	}
}

func TestTokenSessionLifecycleAndExpiry(t *testing.T) {
	store, now := openAuthTestStore(t)
	ctx := t.Context()

	tok, err := store.CreateToken(ctx, "alice", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authenticate(ctx, tok)
	if err != nil || auth.ID == "" {
		t.Fatalf("Authenticate = %+v, err = %v", auth, err)
	}

	secret, sess, err := store.CreateTokenSession(ctx, auth.ID, "Mozilla/5.0", "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, SessionPrefix) || !strings.HasPrefix(sess.ID, "ses_") || sess.TokenID != auth.ID {
		t.Fatalf("session = %+v, secret = %q", sess, secret)
	}

	// Raw API token is not accepted as a session secret.
	if _, err := store.AuthenticateSession(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AuthenticateSession(raw token) = %v, want ErrNotFound", err)
	}

	// Valid session authenticates and returns mapped admin role.
	got, err := store.AuthenticateSession(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != sess.ID || got.AuthMethod != AuthMethodToken || got.Name != "alice" || got.Role != UserRoleAdmin || got.TokenID != auth.ID {
		t.Fatalf("SessionAuth = %+v", got)
	}

	// Activity within 1 minute does not advance last_seen_at.
	*now = now.Add(30 * time.Second)
	if _, err := store.AuthenticateSession(ctx, secret); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListActiveSessionsForToken(ctx, auth.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	if !listed[0].LastSeenAt.Equal(sess.CreatedAt) {
		t.Fatalf("last_seen_at updated too early: got %v, want %v", listed[0].LastSeenAt, sess.CreatedAt)
	}

	// Activity after 5 days slides idle_expires_at forward.
	*now = sess.CreatedAt.Add(5 * 24 * time.Hour)
	if _, err := store.AuthenticateSession(ctx, secret); err != nil {
		t.Fatal(err)
	}
	listed, err = store.ListActiveSessionsForToken(ctx, auth.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	wantIdle := now.Add(SessionIdleTTL)
	if !listed[0].LastSeenAt.Equal(*now) || !listed[0].IdleExpiresAt.Equal(wantIdle) {
		t.Fatalf("sliding expiry not updated: last_seen=%v idle=%v want_idle=%v", listed[0].LastSeenAt, listed[0].IdleExpiresAt, wantIdle)
	}

	// Idle expiration after >7 days without activity rejects session.
	*now = now.Add(SessionIdleTTL + time.Second)
	if _, err := store.AuthenticateSession(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("idle expired session err = %v, want ErrNotFound", err)
	}

	// Absolute expiration after 30 days even with frequent activity.
	*now = sess.CreatedAt
	secret2, _, err := store.CreateTokenSession(ctx, auth.ID, "UA", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 29; day++ {
		*now = sess.CreatedAt.Add(time.Duration(day) * 24 * time.Hour)
		if _, err := store.AuthenticateSession(ctx, secret2); err != nil {
			t.Fatalf("day %d failed: %v", day, err)
		}
	}
	*now = sess.CreatedAt.Add(SessionAbsoluteTTL + time.Second)
	if _, err := store.AuthenticateSession(ctx, secret2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absolute expired session err = %v, want ErrNotFound", err)
	}
}

func TestTokenRevocationAndRegenerationCascadeToSessions(t *testing.T) {
	store, _ := openAuthTestStore(t)
	ctx := t.Context()

	tokA, err := store.CreateToken(ctx, "alice", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	tokB, err := store.CreateToken(ctx, "bob", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	authA, _ := store.Authenticate(ctx, tokA)
	authB, _ := store.Authenticate(ctx, tokB)

	secA, _, err := store.CreateTokenSession(ctx, authA.ID, "UA", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	secB, _, err := store.CreateTokenSession(ctx, authB.ID, "UA", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	// Regenerating alice revokes alice's session but preserves bob's.
	_, newID, err := store.RegenerateTokenWithID(ctx, "alice")
	if err != nil || newID == "" || newID == authA.ID {
		t.Fatalf("RegenerateTokenWithID = %q, err = %v", newID, err)
	}
	if _, err := store.AuthenticateSession(ctx, secA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("regenerated token session still active: %v", err)
	}
	if _, err := store.AuthenticateSession(ctx, secB); err != nil {
		t.Fatalf("unrelated session failed after alice regenerate: %v", err)
	}

	// Revoking bob revokes bob's session.
	if err := store.RevokeToken(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateSession(ctx, secB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token session still active: %v", err)
	}

	// DeleteToken works even when sessions reference the token (PRAGMA foreign_keys = ON).
	if err := store.DeleteToken(ctx, "bob"); err != nil {
		t.Fatalf("DeleteToken with referencing session failed: %v", err)
	}
}

func TestPruneExpiredAuthAndHasActiveRoleAllToken(t *testing.T) {
	store, now := openAuthTestStore(t)
	ctx := t.Context()

	has, err := store.HasActiveRoleAllToken(ctx)
	if err != nil || has {
		t.Fatalf("HasActiveRoleAllToken empty = %v, %v", has, err)
	}
	if _, err := store.CreateToken(ctx, "legacy", RoleAll); err != nil {
		t.Fatal(err)
	}
	has, err = store.HasActiveRoleAllToken(ctx)
	if err != nil || !has {
		t.Fatalf("HasActiveRoleAllToken = %v, %v", has, err)
	}
	if err := store.RevokeToken(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	has, err = store.HasActiveRoleAllToken(ctx)
	if err != nil || has {
		t.Fatalf("HasActiveRoleAllToken after revoke = %v, %v", has, err)
	}

	// Seed an oauth_transaction and a revoked session, then prune after 31 days.
	tok, err := store.CreateToken(ctx, "alice", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := store.Authenticate(ctx, tok)
	_, sess, err := store.CreateTokenSession(ctx, auth.ID, "UA", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q().ExecContext(ctx, `INSERT INTO oauth_transactions(id_hash,provider,state,pkce_verifier,return_to,expires_at,created_at) VALUES('h1','github','st','ver','/',?,?)`,
		stamp(now.Add(10*time.Minute)), stamp(*now)); err != nil {
		t.Fatal(err)
	}

	*now = now.Add(31 * 24 * time.Hour)
	if err := store.PruneExpiredAuth(ctx); err != nil {
		t.Fatal(err)
	}
	var oauthCount, sessCount int
	_ = store.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_transactions`).Scan(&oauthCount)
	_ = store.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&sessCount)
	if oauthCount != 0 || sessCount != 0 {
		t.Fatalf("after prune: oauth=%d sessions=%d, want 0, 0", oauthCount, sessCount)
	}
}

func TestTruncateFieldUTF8Boundary(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		want     string
	}{
		{
			name:     "empty and whitespace",
			input:    "   \t\n  ",
			maxBytes: 10,
			want:     "",
		},
		{
			name:     "zero limit",
			input:    "hello",
			maxBytes: 0,
			want:     "",
		},
		{
			name:     "negative limit",
			input:    "hello",
			maxBytes: -5,
			want:     "",
		},
		{
			name:     "within limit trims whitespace",
			input:    "  héllo 日本語 😀  ",
			maxBytes: 64,
			want:     "héllo 日本語 😀",
		},
		{
			name:     "2-byte rune fits exactly at limit",
			input:    strings.Repeat("a", 254) + "é" + "tail",
			maxBytes: 256,
			want:     strings.Repeat("a", 254) + "é",
		},
		{
			name:     "2-byte rune straddles 256-byte limit",
			input:    strings.Repeat("a", 255) + "é" + "tail",
			maxBytes: 256,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "3-byte rune straddles 256-byte limit by 1 byte",
			input:    strings.Repeat("a", 255) + "日" + "tail",
			maxBytes: 256,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "3-byte rune straddles 256-byte limit by 2 bytes",
			input:    strings.Repeat("a", 254) + "日" + "tail",
			maxBytes: 256,
			want:     strings.Repeat("a", 254),
		},
		{
			name:     "3-byte rune fits exactly at 256-byte limit",
			input:    strings.Repeat("a", 253) + "日" + "tail",
			maxBytes: 256,
			want:     strings.Repeat("a", 253) + "日",
		},
		{
			name:     "4-byte emoji straddles 512-byte limit by 1 byte",
			input:    strings.Repeat("a", 511) + "😀" + "tail",
			maxBytes: 512,
			want:     strings.Repeat("a", 511),
		},
		{
			name:     "4-byte emoji straddles 512-byte limit by 2 bytes",
			input:    strings.Repeat("a", 510) + "😀" + "tail",
			maxBytes: 512,
			want:     strings.Repeat("a", 510),
		},
		{
			name:     "4-byte emoji straddles 512-byte limit by 3 bytes",
			input:    strings.Repeat("a", 509) + "😀" + "tail",
			maxBytes: 512,
			want:     strings.Repeat("a", 509),
		},
		{
			name:     "4-byte emoji fits exactly at 512-byte limit",
			input:    strings.Repeat("a", 508) + "😀" + "tail",
			maxBytes: 512,
			want:     strings.Repeat("a", 508) + "😀",
		},
		{
			name:     "all multi-byte runes exceeding limit",
			input:    strings.Repeat("🚀", 80), // 320 bytes
			maxBytes: 256,
			want:     strings.Repeat("🚀", 64), // 256 bytes
		},
		{
			name:     "all 3-byte runes exceeding 256",
			input:    strings.Repeat("日", 100), // 300 bytes
			maxBytes: 256,
			want:     strings.Repeat("日", 85), // 255 bytes
		},
		{
			name:     "single rune larger than limit",
			input:    "😀",
			maxBytes: 3,
			want:     "",
		},
		{
			name:     "invalid utf8 input stripped before truncation",
			input:    "ok\xff\xfe" + strings.Repeat("a", 10),
			maxBytes: 8,
			want:     "okaaaaaa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateField(tc.input, tc.maxBytes)
			if !utf8.ValidString(got) {
				t.Fatalf("truncateField(%q, %d) returned invalid UTF-8: %x", tc.input, tc.maxBytes, got)
			}
			if tc.maxBytes >= 0 && len(got) > tc.maxBytes {
				t.Fatalf("len(truncateField) = %d, want <= %d", len(got), tc.maxBytes)
			}
			if got != tc.want {
				t.Fatalf("truncateField = %q (len %d), want %q (len %d)", got, len(got), tc.want, len(tc.want))
			}
		})
	}
}

func TestAuthMultibyteUTF8Truncation(t *testing.T) {
	store, _ := openAuthTestStore(t)
	ctx := t.Context()

	// Display names with multi-byte UTF-8 runes straddling the 256-byte boundary.
	initialName := strings.Repeat("a", 255) + "é" + " overflow"
	wantInitialName := strings.Repeat("a", 255)

	res, err := store.ResolveGitHubLogin(ctx, GitHubProfile{
		ID:             "1001",
		Login:          "utf8admin",
		Name:           initialName,
		PrimaryEmail:   "utf8@example.com",
		VerifiedEmails: []string{"utf8@example.com"},
	}, "github:utf8admin", "")
	if err != nil {
		t.Fatalf("ResolveGitHubLogin bootstrap: %v", err)
	}
	if !utf8.ValidString(res.User.Name) || len(res.User.Name) > 256 || res.User.Name != wantInitialName {
		t.Fatalf("bootstrap User.Name = %q (valid=%v len=%d), want %q", res.User.Name, utf8.ValidString(res.User.Name), len(res.User.Name), wantInitialName)
	}

	// Re-login refreshes display name via UPDATE users with a 4-byte emoji straddling 256 bytes.
	refreshedName := strings.Repeat("b", 254) + "😀" + " overflow"
	wantRefreshedName := strings.Repeat("b", 254)
	res2, err := store.ResolveGitHubLogin(ctx, GitHubProfile{
		ID:             "1001",
		Login:          "utf8admin",
		Name:           refreshedName,
		PrimaryEmail:   "utf8@example.com",
		VerifiedEmails: []string{"utf8@example.com"},
	}, "github:utf8admin", "")
	if err != nil {
		t.Fatalf("ResolveGitHubLogin refresh: %v", err)
	}
	if !utf8.ValidString(res2.User.Name) || len(res2.User.Name) > 256 || res2.User.Name != wantRefreshedName {
		t.Fatalf("refresh User.Name = %q (valid=%v len=%d), want %q", res2.User.Name, utf8.ValidString(res2.User.Name), len(res2.User.Name), wantRefreshedName)
	}

	storedUser, err := store.FindUserByIDOrLogin(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("FindUserByIDOrLogin: %v", err)
	}
	if !utf8.ValidString(storedUser.Name) || storedUser.Name != wantRefreshedName {
		t.Fatalf("stored User.Name = %q (valid=%v), want %q", storedUser.Name, utf8.ValidString(storedUser.Name), wantRefreshedName)
	}

	// User-Agent with multi-byte UTF-8 rune straddling the 512-byte boundary for user session.
	userUA := strings.Repeat("u", 510) + "😀" + " extra"
	wantUserUA := strings.Repeat("u", 510)
	userSecret, userSess, err := store.CreateUserSession(ctx, res.User.ID, userUA, "203.0.113.10")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if !utf8.ValidString(userSess.UserAgent) || len(userSess.UserAgent) > 512 || userSess.UserAgent != wantUserUA {
		t.Fatalf("CreateUserSession UserAgent = %q (valid=%v len=%d), want %q", userSess.UserAgent, utf8.ValidString(userSess.UserAgent), len(userSess.UserAgent), wantUserUA)
	}
	if _, err := store.AuthenticateSession(ctx, userSecret); err != nil {
		t.Fatalf("AuthenticateSession(user): %v", err)
	}
	userSessions, err := store.ListActiveSessionsForUser(ctx, res.User.ID)
	if err != nil || len(userSessions) != 1 || userSessions[0].UserAgent != wantUserUA {
		t.Fatalf("ListActiveSessionsForUser = %+v, err = %v", userSessions, err)
	}

	// User-Agent with multi-byte UTF-8 rune straddling the 512-byte boundary for token session.
	tok, err := store.CreateToken(ctx, "utf8-op", RoleOperator)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	tokAuth, err := store.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	tokenUA := strings.Repeat("t", 511) + "日" + " extra"
	wantTokenUA := strings.Repeat("t", 511)
	tokSecret, tokSess, err := store.CreateTokenSession(ctx, tokAuth.ID, tokenUA, "203.0.113.11")
	if err != nil {
		t.Fatalf("CreateTokenSession: %v", err)
	}
	if !utf8.ValidString(tokSess.UserAgent) || len(tokSess.UserAgent) > 512 || tokSess.UserAgent != wantTokenUA {
		t.Fatalf("CreateTokenSession UserAgent = %q (valid=%v len=%d), want %q", tokSess.UserAgent, utf8.ValidString(tokSess.UserAgent), len(tokSess.UserAgent), wantTokenUA)
	}
	if _, err := store.AuthenticateSession(ctx, tokSecret); err != nil {
		t.Fatalf("AuthenticateSession(token): %v", err)
	}
	tokSessions, err := store.ListActiveSessionsForToken(ctx, tokAuth.ID)
	if err != nil || len(tokSessions) != 1 || tokSessions[0].UserAgent != wantTokenUA {
		t.Fatalf("ListActiveSessionsForToken = %+v, err = %v", tokSessions, err)
	}
}
