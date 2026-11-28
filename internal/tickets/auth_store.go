package tickets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrInviteConflict is returned when creating an invite for a target that
	// already belongs to an active or disabled user.
	ErrInviteConflict = errors.New("a user matching this target already exists")
	// ErrUserDisabled is returned when a disabled user attempts to sign in.
	ErrUserDisabled = errors.New("user account is disabled")
	// ErrNotInvited is returned when an unknown GitHub account has no matching invite.
	ErrNotInvited = errors.New("not invited")
	// ErrLastAdmin is returned when a role change or disable would leave zero active admins.
	ErrLastAdmin = errors.New("cannot demote or disable the last active admin")
)

const (
	// SessionPrefix prefixes opaque random session cookie values.
	SessionPrefix = "mses_"
	// OAuthCookiePrefix prefixes opaque random OAuth transaction cookie values.
	OAuthCookiePrefix = "moauth_"
	// InvitePrefix prefixes opaque one-time invite URL fragment secrets.
	InvitePrefix = "minv_"

	// SessionIdleTTL is the sliding idle expiration for browser sessions.
	SessionIdleTTL = 7 * 24 * time.Hour
	// SessionAbsoluteTTL is the hard lifetime cap for browser sessions.
	SessionAbsoluteTTL = 30 * 24 * time.Hour
	// OAuthTransactionTTL is how long an OAuth login handshake remains valid.
	OAuthTransactionTTL = 10 * time.Minute
	// InviteTTL is how long an invite link remains valid.
	InviteTTL = 7 * 24 * time.Hour
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

// User is a human team member authenticated via GitHub OAuth.
type User struct {
	ID          string     `json:"id"`
	Role        string     `json:"role"`
	GitHubID    string     `json:"-"`
	GitHubLogin string     `json:"github_login"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	AvatarURL   string     `json:"avatar_url"`
	InvitedBy   string     `json:"invited_by,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at,omitempty"`
}

// Invite is a pending or completed targeted team invitation.
type Invite struct {
	ID          string     `json:"id"`
	Role        string     `json:"role"`
	GitHubLogin string     `json:"github_login,omitempty"`
	Email       string     `json:"email,omitempty"`
	CreatedBy   string     `json:"created_by"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AcceptedBy  string     `json:"accepted_by,omitempty"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// OAuthTransaction stores server-side state for an in-flight OAuth handshake.
type OAuthTransaction struct {
	Provider     string
	State        string
	PKCEVerifier string
	ReturnTo     string
	InviteID     string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// GitHubProfile holds the verified identity returned by GitHub's OAuth APIs.
type GitHubProfile struct {
	ID             string
	Login          string
	Name           string
	AvatarURL      string
	PrimaryEmail   string
	VerifiedEmails []string
}

// GitHubLoginResult describes the outcome of resolving a GitHub OAuth login.
type GitHubLoginResult struct {
	User     User
	Outcome  string // "existing", "bootstrapped", "invited", "denied_disabled", "denied_not_invited"
	InviteID string
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
	if maxBytes <= 0 {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sanitizeAvatarURL keeps only https:// URLs <= 512 bytes.
func sanitizeAvatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return raw
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
	_, err = s.q().ExecContext(ctx,
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

// CreateUserSession creates a browser session backed by a human user account.
// It returns the opaque cookie secret (mses_<64 hex>) once.
func (s *Store) CreateUserSession(ctx context.Context, userID, userAgent, ipAddress string) (string, Session, error) {
	if strings.TrimSpace(userID) == "" {
		return "", Session{}, errors.New("user ID is required")
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
	_, err = s.q().ExecContext(ctx,
		`INSERT INTO sessions(id,token_hash,auth_method,user_id,token_id,user_agent,ip_address,created_at,last_seen_at,idle_expires_at,expires_at)
		 VALUES(?,?,?,?,NULL,?,?,?,?,?,?)`,
		id, hashToken(secret), AuthMethodGitHub, userID, ua, ip, stamp(now), stamp(now), stamp(idleExp), stamp(absExp),
	)
	if err != nil {
		return "", Session{}, err
	}
	return secret, Session{
		ID:            id,
		AuthMethod:    AuthMethodGitHub,
		UserID:        userID,
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
		tName    sql.NullString
		tRole    sql.NullString
		tScope   sql.NullString
		tRevoked sql.NullString
		// joined user columns
		uRole     sql.NullString
		uLogin    sql.NullString
		uEmail    sql.NullString
		uName     sql.NullString
		uAvatar   sql.NullString
		uDisabled sql.NullString
	)
	err := s.q().QueryRowContext(ctx, `
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
		_, err = s.q().ExecContext(ctx,
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
	res, err := s.q().ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, stamp(s.now()), sessionID)
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

// RevokeSessionForOwner revokes sessionID only if it belongs to the specified
// userID (for GitHub sessions) or tokenID (for token sessions).
func (s *Store) RevokeSessionForOwner(ctx context.Context, sessionID, userID, tokenID string) error {
	var (
		res sql.Result
		err error
	)
	now := stamp(s.now())
	switch {
	case userID != "":
		res, err = s.q().ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`, now, sessionID, userID)
	case tokenID != "":
		res, err = s.q().ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND token_id=? AND revoked_at IS NULL`, now, sessionID, tokenID)
	default:
		return ErrNotFound
	}
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
	rows, err := s.q().QueryContext(ctx, `
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
	rows, err := s.q().QueryContext(ctx, `
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
			sess     Session
			userID   sql.NullString
			tokenID  sql.NullString
			created  string
			lastSeen string
			idleExp  string
			absExp   string
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

// CreateOAuthTransaction persists a short-lived server-side OAuth handshake
// state and returns the opaque cookie secret (moauth_<64 hex>).
func (s *Store) CreateOAuthTransaction(ctx context.Context, provider, state, pkceVerifier, returnTo, inviteID string) (string, error) {
	secretHex, err := randomHex(32)
	if err != nil {
		return "", err
	}
	secret := OAuthCookiePrefix + secretHex
	now := s.now().UTC()
	exp := now.Add(OAuthTransactionTTL)
	if returnTo == "" {
		returnTo = "/"
	}
	var inviteArg any
	if inviteID != "" {
		inviteArg = inviteID
	}
	_, err = s.q().ExecContext(ctx, `
		INSERT INTO oauth_transactions(id_hash, provider, state, pkce_verifier, return_to, invite_id, expires_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		hashToken(secret), provider, state, pkceVerifier, returnTo, inviteArg, stamp(exp), stamp(now),
	)
	if err != nil {
		return "", err
	}
	return secret, nil
}

// ConsumeOAuthTransaction atomically reads and deletes an unexpired OAuth
// transaction by its cookie secret, preventing callback replay.
func (s *Store) ConsumeOAuthTransaction(ctx context.Context, secret string) (OAuthTransaction, error) {
	if !strings.HasPrefix(secret, OAuthCookiePrefix) {
		return OAuthTransaction{}, ErrNotFound
	}
	hash := hashToken(secret)
	nowStamp := stamp(s.now().UTC())
	var (
		out        OAuthTransaction
		inviteID   sql.NullString
		expiresStr string
		createdStr string
	)
	err := s.q().QueryRowContext(ctx, `
		DELETE FROM oauth_transactions
		WHERE id_hash = ? AND expires_at > ?
		RETURNING provider, state, pkce_verifier, return_to, invite_id, expires_at, created_at`,
		hash, nowStamp,
	).Scan(&out.Provider, &out.State, &out.PKCEVerifier, &out.ReturnTo, &inviteID, &expiresStr, &createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthTransaction{}, ErrNotFound
	}
	if err != nil {
		return OAuthTransaction{}, err
	}
	out.InviteID = inviteID.String
	out.ExpiresAt = *parseTime(sql.NullString{String: expiresStr, Valid: true})
	out.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
	return out, nil
}

// CreateInvite creates a targeted invite for either a GitHub handle OR an email
// address (never both, never neither). If an active or disabled user already
// matches the target, ErrInviteConflict is returned. Any prior pending invite
// for the same target is atomically revoked.
func (s *Store) CreateInvite(ctx context.Context, role, githubLogin, email, createdBy string) (Invite, string, error) {
	if !ValidUserRole(role) {
		return Invite{}, "", fmt.Errorf("invalid role %q; must be admin, member, or viewer", role)
	}
	githubLogin = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(githubLogin), "@"))
	email = strings.ToLower(strings.TrimSpace(email))
	if (githubLogin == "" && email == "") || (githubLogin != "" && email != "") {
		return Invite{}, "", errors.New("specify exactly one of github_login or email")
	}
	if githubLogin != "" && strings.ContainsAny(githubLogin, " \t\r\n/") {
		return Invite{}, "", errors.New("invalid github_login")
	}
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n")) {
		return Invite{}, "", errors.New("invalid email address")
	}
	if strings.TrimSpace(createdBy) == "" {
		createdBy = "cli"
	}

	idHex, err := randomHex(16)
	if err != nil {
		return Invite{}, "", err
	}
	secretHex, err := randomHex(32)
	if err != nil {
		return Invite{}, "", err
	}
	id := "inv_" + idHex
	secret := InvitePrefix + secretHex
	now := s.now().UTC()
	exp := now.Add(InviteTTL)
	nowStamp := stamp(now)
	expStamp := stamp(exp)

	err = s.inTx(ctx, func(tx querier) error {
		// Serialize invite mutations against concurrent invites, logins, and user disables.
		if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET id = id WHERE id = 1`); err != nil {
			return err
		}
		var existing int
		if githubLogin != "" {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE LOWER(github_login) = ?`, githubLogin).Scan(&existing); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE LOWER(email) = ?`, email).Scan(&existing); err != nil {
				return err
			}
		}
		if existing > 0 {
			return ErrInviteConflict
		}
		// Revoke any prior pending invite for the same target.
		if githubLogin != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=? WHERE github_login=? AND accepted_at IS NULL AND revoked_at IS NULL`, nowStamp, githubLogin); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=? WHERE email=? AND accepted_at IS NULL AND revoked_at IS NULL`, nowStamp, email); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO invites(id, role, github_login, email, token_hash, created_by, expires_at, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			id, role, githubLogin, email, hashToken(secret), createdBy, expStamp, nowStamp,
		)
		return err
	})
	if err != nil {
		return Invite{}, "", err
	}
	return Invite{
		ID:          id,
		Role:        role,
		GitHubLogin: githubLogin,
		Email:       email,
		CreatedBy:   createdBy,
		ExpiresAt:   exp,
		CreatedAt:   now,
	}, secret, nil
}

// ListPendingInvites returns unexpired, unaccepted, unrevoked invites, newest first.
func (s *Store) ListPendingInvites(ctx context.Context) ([]Invite, error) {
	nowStamp := stamp(s.now().UTC())
	rows, err := s.q().QueryContext(ctx, `
		SELECT id, role, github_login, email, created_by, expires_at, created_at
		FROM invites
		WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC, id DESC`,
		nowStamp,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invites := []Invite{}
	for rows.Next() {
		var inv Invite
		var expStr, createdStr string
		if err := rows.Scan(&inv.ID, &inv.Role, &inv.GitHubLogin, &inv.Email, &inv.CreatedBy, &expStr, &createdStr); err != nil {
			return nil, err
		}
		inv.ExpiresAt = *parseTime(sql.NullString{String: expStr, Valid: true})
		inv.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// RevokeInvite revokes a pending invite by ID.
func (s *Store) RevokeInvite(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx querier) error {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET id = id WHERE id = 1`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=? WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL`, stamp(s.now().UTC()), id)
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
	})
}

// RegenerateInvite rotates an unexpired pending invite's secret and extends
// its expiry by InviteTTL, returning the updated Invite and new one-time secret.
func (s *Store) RegenerateInvite(ctx context.Context, id string) (Invite, string, error) {
	secretHex, err := randomHex(32)
	if err != nil {
		return Invite{}, "", err
	}
	secret := InvitePrefix + secretHex
	now := s.now().UTC()
	exp := now.Add(InviteTTL)
	var inv Invite
	err = s.inTx(ctx, func(tx querier) error {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET id = id WHERE id = 1`); err != nil {
			return err
		}
		var createdStr string
		err := tx.QueryRowContext(ctx, `
			SELECT id, role, github_login, email, created_by, created_at
			FROM invites
			WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL`,
			id,
		).Scan(&inv.ID, &inv.Role, &inv.GitHubLogin, &inv.Email, &inv.CreatedBy, &createdStr)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET token_hash=?, expires_at=? WHERE id=?`, hashToken(secret), stamp(exp), id); err != nil {
			return err
		}
		inv.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
		inv.ExpiresAt = exp
		return nil
	})
	if err != nil {
		return Invite{}, "", err
	}
	return inv, secret, nil
}

// ValidateInviteSecret checks a one-time invite secret from /#/invite#<secret>.
func (s *Store) ValidateInviteSecret(ctx context.Context, secret string) (Invite, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Invite{}, ErrNotFound
	}
	nowStamp := stamp(s.now().UTC())
	var inv Invite
	var expStr, createdStr string
	err := s.q().QueryRowContext(ctx, `
		SELECT id, role, github_login, email, created_by, expires_at, created_at
		FROM invites
		WHERE token_hash=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
		hashToken(secret), nowStamp,
	).Scan(&inv.ID, &inv.Role, &inv.GitHubLogin, &inv.Email, &inv.CreatedBy, &expStr, &createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	inv.ExpiresAt = *parseTime(sql.NullString{String: expStr, Valid: true})
	inv.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
	return inv, nil
}

// ResolveGitHubLogin resolves a verified GitHub OAuth profile inside a single
// transaction locked on auth_state:
//  1. Existing user by immutable github_id -> disabled check or profile refresh.
//  2. One-time bootstrap_admin if auth_state.bootstrapped_at IS NULL.
//  3. Matching pending unexpired invite (specific inviteID if set, else auto-match).
//  4. Otherwise denied (ErrNotInvited).
func (s *Store) ResolveGitHubLogin(ctx context.Context, profile GitHubProfile, bootstrapAdmin, inviteID string) (GitHubLoginResult, error) {
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Login) == "" {
		return GitHubLoginResult{}, errors.New("github profile requires id and login")
	}
	login := strings.TrimSpace(profile.Login)
	loginLower := strings.ToLower(login)
	displayName := truncateField(profile.Name, 256)
	avatarURL := sanitizeAvatarURL(profile.AvatarURL)
	primaryEmail := strings.ToLower(strings.TrimSpace(profile.PrimaryEmail))
	verifiedEmails := make([]string, 0, len(profile.VerifiedEmails))
	for _, em := range profile.VerifiedEmails {
		em = strings.ToLower(strings.TrimSpace(em))
		if em != "" && !slices.Contains(verifiedEmails, em) {
			verifiedEmails = append(verifiedEmails, em)
		}
	}
	if primaryEmail != "" && !slices.Contains(verifiedEmails, primaryEmail) {
		primaryEmail = ""
	}
	if primaryEmail == "" && len(verifiedEmails) > 0 {
		primaryEmail = verifiedEmails[0]
	}

	now := s.now().UTC()
	nowStamp := stamp(now)
	var result GitHubLoginResult
	var retErr error

	err := s.inTx(ctx, func(tx querier) error {
		// Serialize bootstrap/invite provisioning on the singleton auth_state row.
		if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET id = id WHERE id = 1`); err != nil {
			return err
		}

		// 1. Existing user lookup strictly by immutable numeric github_id.
		var (
			u          User
			disabled   sql.NullString
			createdStr string
			updatedStr string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT id, role, github_id, github_login, email, name, avatar_url, invited_by, disabled_at, created_at, updated_at
			FROM users WHERE github_id = ?`,
			profile.ID,
		).Scan(&u.ID, &u.Role, &u.GitHubID, &u.GitHubLogin, &u.Email, &u.Name, &u.AvatarURL, &u.InvitedBy, &disabled, &createdStr, &updatedStr)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			u.DisabledAt = parseTime(disabled)
			u.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
			u.UpdatedAt = *parseTime(sql.NullString{String: updatedStr, Valid: true})
			if u.DisabledAt != nil {
				result = GitHubLoginResult{User: u, Outcome: "denied_disabled"}
				retErr = ErrUserDisabled
				return nil
			}
			// Active user: refresh profile fields (never overwrite role or consume invite).
			emailToStore := u.Email
			if primaryEmail != "" {
				emailToStore = primaryEmail
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE users SET github_login=?, email=?, name=?, avatar_url=?, updated_at=?
				WHERE id=?`,
				login, emailToStore, displayName, avatarURL, nowStamp, u.ID,
			); err != nil {
				return err
			}
			u.GitHubLogin = login
			u.Email = emailToStore
			u.Name = displayName
			u.AvatarURL = avatarURL
			u.UpdatedAt = now
			result = GitHubLoginResult{User: u, Outcome: "existing"}
			return nil
		}

		// 2. One-time bootstrap check via auth_state.bootstrapped_at.
		var bootstrappedAt sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT bootstrapped_at FROM auth_state WHERE id = 1`).Scan(&bootstrappedAt); err != nil {
			return err
		}
		if !bootstrappedAt.Valid && bootstrapAdmin != "" && strings.EqualFold(bootstrapAdmin, "github:"+loginLower) {
			idHex, err := randomHex(16)
			if err != nil {
				return err
			}
			u = User{
				ID:          "usr_" + idHex,
				Role:        UserRoleAdmin,
				GitHubID:    profile.ID,
				GitHubLogin: login,
				Email:       primaryEmail,
				Name:        displayName,
				AvatarURL:   avatarURL,
				InvitedBy:   "bootstrap",
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET bootstrapped_at=? WHERE id=1 AND bootstrapped_at IS NULL`, nowStamp); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO users(id, role, github_id, github_login, email, name, avatar_url, invited_by, created_at, updated_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				u.ID, u.Role, u.GitHubID, u.GitHubLogin, u.Email, u.Name, u.AvatarURL, u.InvitedBy, nowStamp, nowStamp,
			); err != nil {
				return err
			}
			result = GitHubLoginResult{User: u, Outcome: "bootstrapped"}
			return nil
		}

		// 3. Pending unexpired invite matching.
		var matched *Invite
		if inviteID != "" {
			var inv Invite
			err := tx.QueryRowContext(ctx, `
				SELECT id, role, github_login, email, created_by
				FROM invites
				WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
				inviteID, nowStamp,
			).Scan(&inv.ID, &inv.Role, &inv.GitHubLogin, &inv.Email, &inv.CreatedBy)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && inviteMatchesProfile(inv, loginLower, verifiedEmails) {
				matched = &inv
			}
		} else {
			rows, err := tx.QueryContext(ctx, `
				SELECT id, role, github_login, email, created_by
				FROM invites
				WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?
				ORDER BY created_at DESC, id DESC`,
				nowStamp,
			)
			if err != nil {
				return err
			}
			for rows.Next() {
				var inv Invite
				if err := rows.Scan(&inv.ID, &inv.Role, &inv.GitHubLogin, &inv.Email, &inv.CreatedBy); err != nil {
					rows.Close()
					return err
				}
				if inviteMatchesProfile(inv, loginLower, verifiedEmails) {
					found := inv
					matched = &found
					break
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}

		if matched == nil {
			result = GitHubLoginResult{Outcome: "denied_not_invited"}
			retErr = ErrNotInvited
			return nil
		}

		idHex, err := randomHex(16)
		if err != nil {
			return err
		}
		u = User{
			ID:          "usr_" + idHex,
			Role:        matched.Role,
			GitHubID:    profile.ID,
			GitHubLogin: login,
			Email:       primaryEmail,
			Name:        displayName,
			AvatarURL:   avatarURL,
			InvitedBy:   matched.CreatedBy,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO users(id, role, github_id, github_login, email, name, avatar_url, invited_by, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Role, u.GitHubID, u.GitHubLogin, u.Email, u.Name, u.AvatarURL, u.InvitedBy, nowStamp, nowStamp,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET accepted_by=?, accepted_at=? WHERE id=?`, u.ID, nowStamp, matched.ID); err != nil {
			return err
		}
		result = GitHubLoginResult{User: u, Outcome: "invited", InviteID: matched.ID}
		return nil
	})
	if err != nil {
		return GitHubLoginResult{}, err
	}
	return result, retErr
}

func inviteMatchesProfile(inv Invite, loginLower string, verifiedEmails []string) bool {
	if inv.GitHubLogin != "" {
		return strings.EqualFold(inv.GitHubLogin, loginLower)
	}
	if inv.Email != "" {
		return slices.Contains(verifiedEmails, strings.ToLower(inv.Email))
	}
	return false
}

// ListUsers returns all team members ordered active first, then oldest first,
// along with each member's most recent session activity timestamp.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q().QueryContext(ctx, `
		SELECT
			u.id, u.role, u.github_id, u.github_login, u.email, u.name, u.avatar_url,
			u.invited_by, u.disabled_at, u.created_at, u.updated_at,
			(SELECT MAX(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id)
		FROM users u
		ORDER BY CASE WHEN u.disabled_at IS NULL THEN 0 ELSE 1 END ASC, u.created_at ASC, u.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var (
			u          User
			ghID       sql.NullString
			disabled   sql.NullString
			createdStr string
			updatedStr string
			lastSeen   sql.NullString
		)
		if err := rows.Scan(&u.ID, &u.Role, &ghID, &u.GitHubLogin, &u.Email, &u.Name, &u.AvatarURL, &u.InvitedBy, &disabled, &createdStr, &updatedStr, &lastSeen); err != nil {
			return nil, err
		}
		u.GitHubID = ghID.String
		u.DisabledAt = parseTime(disabled)
		u.LastSeenAt = parseTime(lastSeen)
		u.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
		u.UpdatedAt = *parseTime(sql.NullString{String: updatedStr, Valid: true})
		users = append(users, u)
	}
	return users, rows.Err()
}

// FindUserByIDOrLogin looks up a user by exact ID or case-insensitive GitHub handle.
func (s *Store) FindUserByIDOrLogin(ctx context.Context, idOrLogin string) (User, error) {
	key := strings.TrimPrefix(strings.TrimSpace(idOrLogin), "@")
	if key == "" {
		return User{}, ErrNotFound
	}
	var (
		u          User
		ghID       sql.NullString
		disabled   sql.NullString
		createdStr string
		updatedStr string
	)
	err := s.q().QueryRowContext(ctx, `
		SELECT id, role, github_id, github_login, email, name, avatar_url, invited_by, disabled_at, created_at, updated_at
		FROM users
		WHERE id = ? OR LOWER(github_login) = LOWER(?)
		LIMIT 1`,
		key, key,
	).Scan(&u.ID, &u.Role, &ghID, &u.GitHubLogin, &u.Email, &u.Name, &u.AvatarURL, &u.InvitedBy, &disabled, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.GitHubID = ghID.String
	u.DisabledAt = parseTime(disabled)
	u.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})
	u.UpdatedAt = *parseTime(sql.NullString{String: updatedStr, Valid: true})
	return u, nil
}

// UpdateUser updates a team member's role and/or disabled status inside an
// auth_state-serialized transaction. Enforces the last-admin invariant (409 /
// ErrLastAdmin) and, when disabling, revokes all active sessions for that user
// and all pending invites matching their github_login or email in one tx.
func (s *Store) UpdateUser(ctx context.Context, id string, role *string, disabled *bool) (User, error) {
	if role == nil && disabled == nil {
		return User{}, errors.New("specify role or disabled")
	}
	if role != nil && !ValidUserRole(*role) {
		return User{}, fmt.Errorf("invalid role %q; must be admin, member, or viewer", *role)
	}
	now := s.now().UTC()
	nowStamp := stamp(now)
	var updated User

	err := s.inTx(ctx, func(tx querier) error {
		// Serialize admin mutations on singleton auth_state row.
		if _, err := tx.ExecContext(ctx, `UPDATE auth_state SET id = id WHERE id = 1`); err != nil {
			return err
		}
		var (
			ghID       sql.NullString
			curDis     sql.NullString
			createdStr string
			updatedStr string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT id, role, github_id, github_login, email, name, avatar_url, invited_by, disabled_at, created_at, updated_at
			FROM users WHERE id = ?`,
			id,
		).Scan(&updated.ID, &updated.Role, &ghID, &updated.GitHubLogin, &updated.Email, &updated.Name, &updated.AvatarURL, &updated.InvitedBy, &curDis, &createdStr, &updatedStr)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		updated.GitHubID = ghID.String
		updated.DisabledAt = parseTime(curDis)
		updated.CreatedAt = *parseTime(sql.NullString{String: createdStr, Valid: true})

		nextRole := updated.Role
		if role != nil {
			nextRole = *role
		}
		nextDisabled := updated.DisabledAt != nil
		if disabled != nil {
			nextDisabled = *disabled
		}

		isActiveAdmin := updated.Role == UserRoleAdmin && updated.DisabledAt == nil
		if isActiveAdmin && (nextRole != UserRoleAdmin || nextDisabled) {
			var activeAdmins int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role=? AND disabled_at IS NULL`, UserRoleAdmin).Scan(&activeAdmins); err != nil {
				return err
			}
			if activeAdmins < 2 {
				return ErrLastAdmin
			}
		}

		var disabledArg any
		if nextDisabled {
			if updated.DisabledAt != nil {
				disabledArg = curDis.String
			} else {
				disabledArg = nowStamp
				ts := now
				updated.DisabledAt = &ts
			}
		} else {
			disabledArg = nil
			updated.DisabledAt = nil
		}

		if _, err := tx.ExecContext(ctx, `UPDATE users SET role=?, disabled_at=?, updated_at=? WHERE id=?`, nextRole, disabledArg, nowStamp, id); err != nil {
			return err
		}
		updated.Role = nextRole
		updated.UpdatedAt = now

		// Offboarding cascade: revoke all active sessions and matching pending invites.
		if disabled != nil && *disabled {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, nowStamp, id); err != nil {
				return err
			}
			if updated.GitHubLogin != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=? WHERE LOWER(github_login)=LOWER(?) AND accepted_at IS NULL AND revoked_at IS NULL`, nowStamp, updated.GitHubLogin); err != nil {
					return err
				}
			}
			if updated.Email != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=? WHERE LOWER(email)=LOWER(?) AND accepted_at IS NULL AND revoked_at IS NULL`, nowStamp, updated.Email); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return updated, nil
}

// PruneExpiredAuth removes expired OAuth transactions and aged expired/revoked sessions.
func (s *Store) PruneExpiredAuth(ctx context.Context) error {
	now := s.now().UTC()
	nowStamp := stamp(now)
	oldStamp := stamp(now.Add(-sessionPruneRetention))
	if _, err := s.q().ExecContext(ctx, `DELETE FROM oauth_transactions WHERE expires_at < ?`, nowStamp); err != nil {
		return err
	}
	_, err := s.q().ExecContext(ctx, `DELETE FROM sessions WHERE (revoked_at IS NOT NULL AND revoked_at < ?) OR expires_at < ?`, oldStamp, oldStamp)
	return err
}
