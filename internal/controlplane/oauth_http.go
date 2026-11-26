package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	oauthCookie       = "machinist_oauth"
	secureOAuthCookie = "__Host-machinist_oauth"

	defaultGitHubOAuthBase = "https://github.com"
	defaultGitHubAPIBase   = "https://api.github.com"
)

func oauthCookieName(cfg config.Config, r *http.Request) string {
	if isHTTPS(cfg, r) {
		return secureOAuthCookie
	}
	return oauthCookie
}

func (s *Server) setOAuthCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	cfg := s.state().config
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookieName(cfg, r),
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(cfg, r),
	})
}

func readOAuthCookie(cfg config.Config, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(oauthCookieName(cfg, r))
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func randomBase64URL(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceS256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sanitizeReturnTo allows only safe relative paths starting with "/" and
// rejects protocol-relative ("//"), backslash, and control characters.
func sanitizeReturnTo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '\\' || c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	return raw
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.state().config
	_, _, err := s.authenticateRequest(r)
	methods := []string{}
	if cfg.Auth.GitHub.Enabled {
		methods = append(methods, "github")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":        err == nil,
		"methods":              methods,
		"token_signin_enabled": cfg.Auth.TokenSigninEnabled(),
	})
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request, _ string) {
	p, ok := principalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("a token is required"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if p.AuthMethod == tickets.AuthMethodGitHub {
		writeJSON(w, http.StatusOK, map[string]any{
			"auth_method": p.AuthMethod,
			"role":        p.Role,
			"version":     s.version,
			"user": map[string]any{
				"id":           p.UserID,
				"github_login": p.GitHubLogin,
				"name":         p.DisplayName,
				"email":        p.Email,
				"avatar_url":   p.AvatarURL,
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_method": tickets.AuthMethodToken,
		"role":        p.Role,
		"version":     s.version,
		"token_name":  p.Name,
	})
}

type sessionView struct {
	ID         string    `json:"id"`
	AuthMethod string    `json:"auth_method"`
	UserAgent  string    `json:"user_agent"`
	IPAddress  string    `json:"ip_address"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Current    bool      `json:"current"`
}

func (s *Server) listMySessions(w http.ResponseWriter, r *http.Request, _ string) {
	p, ok := principalFromRequest(r)
	if !ok || !p.ViaCookie {
		writeError(w, http.StatusForbidden, errors.New("sessions are only available to browser sessions"))
		return
	}
	var (
		sessions []tickets.Session
		err      error
	)
	if p.UserID != "" {
		sessions, err = s.store.ListActiveSessionsForUser(r.Context(), p.UserID)
	} else {
		sessions, err = s.store.ListActiveSessionsForToken(r.Context(), p.TokenID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]sessionView, 0, len(sessions))
	for _, sess := range sessions {
		views = append(views, sessionView{
			ID:         sess.ID,
			AuthMethod: sess.AuthMethod,
			UserAgent:  sess.UserAgent,
			IPAddress:  sess.IPAddress,
			CreatedAt:  sess.CreatedAt,
			LastSeenAt: sess.LastSeenAt,
			Current:    sess.ID == p.SessionID,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) revokeMySession(w http.ResponseWriter, r *http.Request, _ string) {
	p, ok := principalFromRequest(r)
	if !ok || !p.ViaCookie {
		writeError(w, http.StatusForbidden, errors.New("sessions are only available to browser sessions"))
		return
	}
	id := r.PathValue("id")
	if err := s.store.RevokeSessionForOwner(r.Context(), id, p.UserID, p.TokenID); errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if id == p.SessionID {
		s.setSessionCookie(w, r, "", -time.Second)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) prepareInvite(w http.ResponseWriter, r *http.Request) {
	cfg := s.state().config
	if !s.authLimiter.Allow(clientIP(r, cfg.Server.TrustedProxyHops)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts; try again shortly"))
		return
	}
	if !cfg.Auth.GitHub.Enabled {
		writeError(w, http.StatusNotFound, errors.New("GitHub OAuth is not enabled on this instance"))
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &req) {
		return
	}
	inv, err := s.store.ValidateInviteSecret(r.Context(), req.Token)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("This invite link is invalid or has expired."))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Persist invite_id in an OAuth transaction cookie so /auth/github/start
	// picks it up without ever placing invite secrets in URLs or logs.
	cookieSecret, err := s.store.CreateOAuthTransaction(r.Context(), "github", "", "", "/", inv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setOAuthCookie(w, r, cookieSecret, tickets.OAuthTransactionTTL)
	hint := "github:" + inv.GitHubLogin
	if inv.GitHubLogin == "" {
		hint = inv.Email
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":       true,
		"role":        inv.Role,
		"target_hint": hint,
		"start_url":   "/auth/github/start",
	})
}

func (s *Server) githubOAuthStart(w http.ResponseWriter, r *http.Request) {
	cfg := s.state().config
	if !s.authLimiter.Allow(clientIP(r, cfg.Server.TrustedProxyHops)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts; try again shortly"))
		return
	}
	if !cfg.Auth.GitHub.Enabled {
		writeError(w, http.StatusNotFound, errors.New("GitHub OAuth is not enabled on this instance"))
		return
	}
	returnTo := sanitizeReturnTo(r.URL.Query().Get("return_to"))

	// Preserve any invite_id staged by POST /api/v1/invites/prepare.
	var inviteID string
	if existing, ok := readOAuthCookie(cfg, r); ok {
		if prepared, err := s.store.ConsumeOAuthTransaction(r.Context(), existing); err == nil && prepared.InviteID != "" {
			inviteID = prepared.InviteID
		}
	}

	state, err := randomBase64URL(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	verifier, err := randomBase64URL(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cookieSecret, err := s.store.CreateOAuthTransaction(r.Context(), "github", state, verifier, returnTo, inviteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setOAuthCookie(w, r, cookieSecret, tickets.OAuthTransactionTTL)

	oauthBase := s.githubOAuthBase
	if oauthBase == "" {
		oauthBase = defaultGitHubOAuthBase
	}
	callbackURL := strings.TrimRight(cfg.Server.PublicURL, "/") + "/auth/github/callback"
	params := url.Values{
		"client_id":             {cfg.Auth.GitHub.ClientID},
		"redirect_uri":          {callbackURL},
		"scope":                 {"read:user user:email"},
		"state":                 {state},
		"code_challenge":        {pkceS256Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, strings.TrimRight(oauthBase, "/")+"/login/oauth/authorize?"+params.Encode(), http.StatusFound)
}

func (s *Server) githubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	cfg := s.state().config
	if !s.authLimiter.Allow(clientIP(r, cfg.Server.TrustedProxyHops)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts; try again shortly"))
		return
	}
	if !cfg.Auth.GitHub.Enabled {
		writeError(w, http.StatusNotFound, errors.New("GitHub OAuth is not enabled on this instance"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Always clear the single-use OAuth transaction cookie.
	cookieSecret, hasCookie := readOAuthCookie(cfg, r)
	s.setOAuthCookie(w, r, "", -time.Second)

	if errParam := strings.TrimSpace(r.URL.Query().Get("error")); errParam != "" {
		if hasCookie {
			_, _ = s.store.ConsumeOAuthTransaction(r.Context(), cookieSecret)
		}
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "oauth_denied"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_denied", http.StatusFound)
		return
	}
	if !hasCookie {
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "missing_transaction"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_failed", http.StatusFound)
		return
	}
	tx, err := s.store.ConsumeOAuthTransaction(r.Context(), cookieSecret)
	if err != nil || tx.State == "" || tx.PKCEVerifier == "" {
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "invalid_transaction"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_failed", http.StatusFound)
		return
	}
	gotState := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(gotState), []byte(tx.State)) != 1 {
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "state_mismatch"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_failed", http.StatusFound)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "missing_code"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_failed", http.StatusFound)
		return
	}

	profile, err := s.fetchGitHubOAuthProfile(r.Context(), cfg, code, tx.PKCEVerifier)
	if err != nil {
		s.audit(r, "auth.login.denied", "", "", "github", "denied", slog.String("reason", "oauth_exchange_failed"))
		http.Redirect(w, r, "/#/sign-in?error=oauth_failed", http.StatusFound)
		return
	}

	resolved, err := s.store.ResolveGitHubLogin(r.Context(), profile, cfg.Auth.BootstrapAdmin, tx.InviteID)
	if errors.Is(err, tickets.ErrUserDisabled) {
		s.audit(r, "auth.login.denied", resolved.User.ID, resolved.User.GitHubLogin, "github:"+profile.Login, "denied", slog.String("reason", "disabled"))
		http.Redirect(w, r, "/#/sign-in?error=disabled", http.StatusFound)
		return
	}
	if errors.Is(err, tickets.ErrNotInvited) {
		s.audit(r, "auth.login.denied", "", profile.Login, "github:"+profile.Login, "denied", slog.String("reason", "not_invited"))
		http.Redirect(w, r, "/#/sign-in?error=not_invited", http.StatusFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sessionSecret, _, err := s.store.CreateUserSession(r.Context(), resolved.User.ID, r.UserAgent(), clientIP(r, cfg.Server.TrustedProxyHops))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setSessionCookie(w, r, sessionSecret, sessionMaxAge)

	switch resolved.Outcome {
	case "bootstrapped":
		s.audit(r, "member.bootstrapped", resolved.User.ID, resolved.User.GitHubLogin, resolved.User.ID, "succeeded", slog.String("role", resolved.User.Role))
	case "invited":
		s.audit(r, "invite.accepted", resolved.User.ID, resolved.User.GitHubLogin, resolved.InviteID, "succeeded", slog.String("role", resolved.User.Role))
	}
	s.audit(r, "auth.login.succeeded", resolved.User.ID, resolved.User.GitHubLogin, resolved.User.ID, "succeeded", slog.String("method", "github"))

	http.Redirect(w, r, sanitizeReturnTo(tx.ReturnTo), http.StatusFound)
}

func (s *Server) fetchGitHubOAuthProfile(ctx context.Context, cfg config.Config, code, pkceVerifier string) (tickets.GitHubProfile, error) {
	httpClient := s.oauthHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	oauthBase := s.githubOAuthBase
	if oauthBase == "" {
		oauthBase = defaultGitHubOAuthBase
	}
	apiBase := s.githubAPIBase
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}

	callbackURL := strings.TrimRight(cfg.Server.PublicURL, "/") + "/auth/github/callback"
	form := url.Values{
		"client_id":     {cfg.Auth.GitHub.ClientID},
		"client_secret": {cfg.Auth.GitHub.ClientSecret},
		"code":          {code},
		"redirect_uri":  {callbackURL},
		"code_verifier": {pkceVerifier},
	}
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(oauthBase, "/")+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")
	tokenReq.Header.Set("User-Agent", "machinist")

	tokenResp, err := httpClient.Do(tokenReq)
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		return tickets.GitHubProfile{}, fmt.Errorf("github token exchange status %d", tokenResp.StatusCode)
	}
	var tokenBody struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(tokenResp.Body, 64<<10)).Decode(&tokenBody); err != nil {
		return tickets.GitHubProfile{}, err
	}
	if tokenBody.Error != "" || tokenBody.AccessToken == "" {
		return tickets.GitHubProfile{}, errors.New("github token exchange returned no access_token")
	}
	// Access token is used strictly for the two profile requests below and
	// immediately discarded when this function returns — never stored or logged.
	accessToken := tokenBody.AccessToken

	// 1. GET /user — immutable numeric ID, login, name, avatar_url (ignore unverified .email).
	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBase, "/")+"/user", nil)
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	userReq.Header.Set("Authorization", "Bearer "+accessToken)
	userReq.Header.Set("Accept", "application/vnd.github+json")
	userReq.Header.Set("User-Agent", "machinist")
	userResp, err := httpClient.Do(userReq)
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	defer userResp.Body.Close()
	if userResp.StatusCode != http.StatusOK {
		return tickets.GitHubProfile{}, fmt.Errorf("github /user status %d", userResp.StatusCode)
	}
	var ghUser struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(io.LimitReader(userResp.Body, 128<<10)).Decode(&ghUser); err != nil {
		return tickets.GitHubProfile{}, err
	}
	if ghUser.ID <= 0 || strings.TrimSpace(ghUser.Login) == "" {
		return tickets.GitHubProfile{}, errors.New("github /user missing id or login")
	}

	// 2. GET /user/emails — verified emails only (preferring primary && verified).
	var verifiedEmails []string
	var primaryEmail string
	emailsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBase, "/")+"/user/emails", nil)
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	emailsReq.Header.Set("Authorization", "Bearer "+accessToken)
	emailsReq.Header.Set("Accept", "application/vnd.github+json")
	emailsReq.Header.Set("User-Agent", "machinist")
	emailsResp, err := httpClient.Do(emailsReq)
	if err != nil {
		return tickets.GitHubProfile{}, err
	}
	defer emailsResp.Body.Close()
	if emailsResp.StatusCode == http.StatusOK {
		var ghEmails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := json.NewDecoder(io.LimitReader(emailsResp.Body, 256<<10)).Decode(&ghEmails); err != nil {
			return tickets.GitHubProfile{}, err
		}
		for _, em := range ghEmails {
			if !em.Verified {
				continue
			}
			addr := strings.ToLower(strings.TrimSpace(em.Email))
			if addr == "" {
				continue
			}
			verifiedEmails = append(verifiedEmails, addr)
			if em.Primary && primaryEmail == "" {
				primaryEmail = addr
			}
		}
	}

	return tickets.GitHubProfile{
		ID:             strconv.FormatInt(ghUser.ID, 10),
		Login:          ghUser.Login,
		Name:           ghUser.Name,
		AvatarURL:      ghUser.AvatarURL,
		PrimaryEmail:   primaryEmail,
		VerifiedEmails: verifiedEmails,
	}, nil
}
