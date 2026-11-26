package tickets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// SessionPrefix prefixes opaque random session cookie values.
	SessionPrefix = "mses_"

	// SessionIdleTTL is the sliding idle expiration for browser sessions.
	SessionIdleTTL = 7 * 24 * time.Hour
	// SessionAbsoluteTTL is the hard lifetime cap for browser sessions.
	SessionAbsoluteTTL = 30 * 24 * time.Hour
	// sessionPruneRetention keeps revoked/expired sessions briefly before pruning.
	sessionPruneRetention = 30 * 24 * time.Hour

	AuthMethodGitHub = "github"
	AuthMethodToken  = "token"

	UserRoleAdmin  = "admin"
	UserRoleMember = "member"
	UserRoleViewer = "viewer"
)

// RoleToScope maps a human user role to Machinist's operator scope.
func RoleToScope(role string) string {
	switch role {
	case UserRoleAdmin:
		return ScopeAll
	case UserRoleMember:
		return ScopeRun
	case UserRoleViewer:
		return ScopeRead
	default:
		return ""
	}
}

// ScopeToUserRole maps an operator token scope to the equivalent RBAC role.
func ScopeToUserRole(scope string) string {
	switch scope {
	case ScopeAll:
		return UserRoleAdmin
	case ScopeRun:
		return UserRoleMember
	case ScopeRead:
		return UserRoleViewer
	default:
		return UserRoleViewer
	}
}

// ValidUserRole reports whether role is one of admin, member, or viewer.
func ValidUserRole(role string) bool {
	return role == UserRoleAdmin || role == UserRoleMember || role == UserRoleViewer
}

// Session is a browser session persisted in the sessions table.
type Session struct {
	ID            string     `json:"id"`
	AuthMethod    string     `json:"auth_method"`
	UserID        string     `json:"user_id,omitempty"`
	TokenID       string     `json:"token_id,omitempty"`
	UserAgent     string     `json:"user_agent"`
	IPAddress     string     `json:"ip_address"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	IdleExpiresAt time.Time  `json:"idle_expires_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// SessionAuth is the resolved identity of an active browser session.
type SessionAuth struct {
	SessionID   string
	AuthMethod  string
	UserID      string
	TokenID     string
	Name        string
	Role        string
	TokenRole   string
	TokenScope  string
	GitHubLogin string
	Email       string
	DisplayName string
	AvatarURL   string
}

func truncateField(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

// CreateTokenSession creates a browser session backed by an existing operator token.
// It returns the opaque cookie secret (mses_<64 hex>) once.
func (s *Store) CreateTokenSession(ctx context.Context, tokenID, userAgent, ipAddress string) (string, Session, error) {
	if strings.TrimSpace(tokenID) == "" {
		return "", Session{}, errors.New("token ID is required")
	}
	idHex, err := randomHex(16)
	if err != nil {
		return "", Session{}, err
	}
	secretHex, err := randomHex(32)
	if err != nil {
		return "", Session{}, err
	}
	id := "ses_" + idHex
	secret := SessionPrefix + secretHex
	now := s.now().UTC()
	idleExp := now.Add(SessionIdleTTL)
	absExp := now.Add(SessionAbsoluteTTL)
	ua := truncateField(userAgent, 512)
	ip := truncateField(ipAddress, 128)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions(id,token_hash,auth_method,user_id,token_id,user_agent,ip_address,created_at,last_seen_at,idle_expires_at,expires_at)
		 VALUES(?,?,?,NULL,?,?,?,?,?,?,?)`,
		id, hashToken(secret), AuthMethodToken, tokenID, ua, ip, stamp(now), stamp(now), stamp(idleExp), stamp(absExp),
	)
	if err != nil {
		return "", Session{}, err
	}
	return secret, Session{
		ID:            id,
		AuthMethod:    AuthMethodToken,
		TokenID:       tokenID,
		UserAgent:     ua,
		IPAddress:     ip,
		CreatedAt:     now,
		LastSeenAt:    now,
		IdleExpiresAt: idleExp,
		ExpiresAt:     absExp,
	}, nil
}

// AuthenticateSession validates an opaque browser session cookie value, joins
// the owning token or user to enforce live revocation/disabled status and
// roles, and updates last_seen_at/idle_expires_at at most once per minute.
func (s *Store) AuthenticateSession(ctx context.Context, secret string) (SessionAuth, error) {
	if !strings.HasPrefix(secret, SessionPrefix) {
		return SessionAuth{}, ErrNotFound
	}
	now := s.now().UTC()
	nowStamp := stamp(now)
	cutoff := stamp(now.Add(-tokenUseInterval))
	hash := hashToken(secret)

	var (
		auth       SessionAuth
		userID     sql.NullString
		tokenID    sql.NullString
		expiresStr string
		stale      bool
		// joined token columns
		tName      sql.NullString
		tRole      sql.NullString
		tScope     sql.NullString
		tRevoked   sql.NullString
		// joined user columns
		uRole      sql.NullString
		uLogin     sql.NullString
		uEmail     sql.NullString
		uName      sql.NullString
		uAvatar    sql.NullString
		uDisabled  sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.id, s.auth_method, s.user_id, s.token_id, s.expires_at, (s.last_seen_at < ?),
			t.name, t.role, t.scope, t.revoked_at,
			u.role, u.github_login, u.email, u.name, u.avatar_url, u.disabled_at
		FROM sessions s
		LEFT JOIN tokens t ON s.token_id = t.id
		LEFT JOIN users u ON s.user_id = u.id
		WHERE s.token_hash = ?
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > ?
		  AND s.expires_at > ?`,
		cutoff, hash, nowStamp, nowStamp,
	).Scan(
		&auth.SessionID, &auth.AuthMethod, &userID, &tokenID, &expiresStr, &stale,
		&tName, &tRole, &tScope, &tRevoked,
		&uRole, &uLogin, &uEmail, &uName, &uAvatar, &uDisabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionAuth{}, ErrNotFound
	}
	if err != nil {
		return SessionAuth{}, err
	}

	switch auth.AuthMethod {
	case AuthMethodToken:
		if !tokenID.Valid || !tName.Valid || tRevoked.Valid {
			return SessionAuth{}, ErrNotFound
		}
		auth.TokenID = tokenID.String
		auth.Name = tName.String
		auth.TokenRole = tRole.String
		auth.TokenScope = tScope.String
		auth.Role = ScopeToUserRole(tScope.String)
	case AuthMethodGitHub:
		if !userID.Valid || !uRole.Valid || uDisabled.Valid {
			return SessionAuth{}, ErrNotFound
		}
		auth.UserID = userID.String
		auth.Role = uRole.String
		auth.GitHubLogin = uLogin.String
		auth.Email = uEmail.String
		auth.DisplayName = uName.String
		auth.AvatarURL = uAvatar.String
		auth.Name = uLogin.String
		if auth.Name == "" {
			auth.Name = uName.String
		}
	default:
		return SessionAuth{}, fmt.Errorf("unknown session auth_method %q", auth.AuthMethod)
	}

	if stale {
		newIdle := now.Add(SessionIdleTTL)
		if absExp := parseTime(sql.NullString{String: expiresStr, Valid: true}); absExp != nil && newIdle.After(*absExp) {
			newIdle = *absExp
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at=?, idle_expires_at=? WHERE id=? AND revoked_at IS NULL AND last_seen_at<?`,
			nowStamp, stamp(newIdle), auth.SessionID, cutoff,
		)
		if err != nil {
			return SessionAuth{}, err
		}
	}
	return auth, nil
}

// RevokeSession marks a single session revoked immediately.
func (s *Store) RevokeSession(ctx context.Context, sessionID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, stamp(s.now()), sessionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSessionBySecret marks the session matching secret as revoked.
func (s *Store) RevokeSessionBySecret(ctx context.Context, secret string) error {
	if !strings.HasPrefix(secret, SessionPrefix) {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, stamp(s.now()), hashToken(secret))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActiveSessionsForToken lists active unexpired sessions belonging to tokenID.
func (s *Store) ListActiveSessionsForToken(ctx context.Context, tokenID string) ([]Session, error) {
	nowStamp := stamp(s.now())
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, auth_method, user_id, token_id, user_agent, ip_address, created_at, last_seen_at, idle_expires_at, expires_at
		FROM sessions
		WHERE token_id = ? AND revoked_at IS NULL AND idle_expires_at > ? AND expires_at > ?
		ORDER BY last_seen_at DESC, id DESC`,
		tokenID, nowStamp, nowStamp,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

// ListActiveSessionsForUser lists active unexpired sessions belonging to userID.
func (s *Store) ListActiveSessionsForUser(ctx context.Context, userID string) ([]Session, error) {
	nowStamp := stamp(s.now())
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, auth_method, user_id, token_id, user_agent, ip_address, created_at, last_seen_at, idle_expires_at, expires_at
		FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL AND idle_expires_at > ? AND expires_at > ?
		ORDER BY last_seen_at DESC, id DESC`,
		userID, nowStamp, nowStamp,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	out := []Session{}
	for rows.Next() {
		var (
			sess      Session
			userID    sql.NullString
			tokenID   sql.NullString
			created   string
			lastSeen  string
			idleExp   string
			absExp    string
		)
		if err := rows.Scan(&sess.ID, &sess.AuthMethod, &userID, &tokenID, &sess.UserAgent, &sess.IPAddress, &created, &lastSeen, &idleExp, &absExp); err != nil {
			return nil, err
		}
		sess.UserID = userID.String
		sess.TokenID = tokenID.String
		sess.CreatedAt = *parseTime(sql.NullString{String: created, Valid: true})
		sess.LastSeenAt = *parseTime(sql.NullString{String: lastSeen, Valid: true})
		sess.IdleExpiresAt = *parseTime(sql.NullString{String: idleExp, Valid: true})
		sess.ExpiresAt = *parseTime(sql.NullString{String: absExp, Valid: true})
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PruneExpiredAuth removes expired OAuth transactions and aged expired/revoked sessions.
func (s *Store) PruneExpiredAuth(ctx context.Context) error {
	now := s.now().UTC()
	nowStamp := stamp(now)
	oldStamp := stamp(now.Add(-sessionPruneRetention))
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_transactions WHERE expires_at < ?`, nowStamp); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE (revoked_at IS NOT NULL AND revoked_at < ?) OR expires_at < ?`, oldStamp, oldStamp)
	return err
}
