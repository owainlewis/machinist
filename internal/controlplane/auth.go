package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	sessionCookie       = "machinist_session"
	secureSessionCookie = "__Host-machinist_session"
	sessionMaxAge       = tickets.SessionAbsoluteTTL
)

type authenticatedHandler func(w http.ResponseWriter, r *http.Request, tokenName string)

// Principal describes the authenticated caller attached to a request context.
type Principal struct {
	Kind        string // "user" or "token"
	AuthMethod  string // "github" or "token"
	UserID      string
	TokenID     string
	SessionID   string
	Name        string
	Role        string // "admin", "member", or "viewer"
	TokenRole   string // raw token role ("", "operator", "runner")
	TokenScope  string // raw token scope ("", "run", "read")
	GitHubLogin string
	Email       string
	DisplayName string
	AvatarURL   string
	ViaCookie   bool
}

type principalContextKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func principalFromRequest(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(principalContextKey{}).(Principal)
	return p, ok
}

// isHTTPS reports whether the deployment serves HTTPS externally (via
// server.public_url, native TLS config, or a TLS listener).
func isHTTPS(cfg config.Config, r *http.Request) bool {
	return strings.HasPrefix(cfg.Server.PublicURL, "https://") ||
		cfg.Server.TLSCert != "" ||
		(r != nil && r.TLS != nil)
}

// sessionCookieName returns __Host-machinist_session over HTTPS and
// machinist_session over plain HTTP.
func sessionCookieName(cfg config.Config, r *http.Request) string {
	if isHTTPS(cfg, r) {
		return secureSessionCookie
	}
	return sessionCookie
}

// publicAuthActive reports whether public-instance hardening rules (such as
// rejecting legacy RoleAll tokens) are active.
func publicAuthActive(cfg config.Config) bool {
	return cfg.Auth.GitHub.Enabled || isPublicNonLoopback(cfg.Server.PublicURL)
}

// hasBearerHeader reports whether the request carries an Authorization: Bearer
// header. Requests with a Bearer header never fall back to session cookies.
func hasBearerHeader(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// bearerToken reads the bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}

func readSessionCookie(cfg config.Config, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName(cfg, r))
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func (s *Server) authenticateRequest(r *http.Request) (Principal, int, error) {
	cfg := s.state().config
	public := publicAuthActive(cfg)

	if hasBearerHeader(r) {
		token := bearerToken(r)
		if token == "" {
			return Principal{}, http.StatusUnauthorized, errors.New("a token is required")
		}
		auth, err := s.store.Authenticate(r.Context(), token)
		if errors.Is(err, tickets.ErrNotFound) {
			return Principal{}, http.StatusUnauthorized, errors.New("invalid token")
		}
		if err != nil {
			return Principal{}, http.StatusInternalServerError, err
		}
		if auth.Role == tickets.RoleAll && public {
			return Principal{}, http.StatusForbidden, errors.New("legacy unscoped role-less tokens are disabled on public deployments; create a typed operator or runner token")
		}
		return Principal{
			Kind:       "token",
			AuthMethod: tickets.AuthMethodToken,
			TokenID:    auth.ID,
			Name:       auth.Name,
			Role:       tickets.ScopeToUserRole(auth.Scope),
			TokenRole:  auth.Role,
			TokenScope: auth.Scope,
			ViaCookie:  false,
		}, 0, nil
	}

	cookieVal, ok := readSessionCookie(cfg, r)
	if !ok {
		return Principal{}, http.StatusUnauthorized, errors.New("a token is required")
	}
	sess, err := s.store.AuthenticateSession(r.Context(), cookieVal)
	if errors.Is(err, tickets.ErrNotFound) {
		return Principal{}, http.StatusUnauthorized, errors.New("invalid token")
	}
	if err != nil {
		return Principal{}, http.StatusInternalServerError, err
	}
	if sess.AuthMethod == tickets.AuthMethodToken {
		if sess.TokenRole == tickets.RoleAll && public {
			return Principal{}, http.StatusForbidden, errors.New("legacy unscoped role-less tokens are disabled on public deployments; create a typed operator or runner token")
		}
		return Principal{
			Kind:       "token",
			AuthMethod: tickets.AuthMethodToken,
			TokenID:    sess.TokenID,
			SessionID:  sess.SessionID,
			Name:       sess.Name,
			Role:       sess.Role,
			TokenRole:  sess.TokenRole,
			TokenScope: sess.TokenScope,
			ViaCookie:  true,
		}, 0, nil
	}
	return Principal{
		Kind:        "user",
		AuthMethod:  tickets.AuthMethodGitHub,
		UserID:      sess.UserID,
		SessionID:   sess.SessionID,
		Name:        sess.Name,
		Role:        sess.Role,
		TokenRole:   tickets.RoleOperator,
		TokenScope:  tickets.RoleToScope(sess.Role),
		GitHubLogin: sess.GitHubLogin,
		Email:       sess.Email,
		DisplayName: sess.DisplayName,
		AvatarURL:   sess.AvatarURL,
		ViaCookie:   true,
	}, 0, nil
}

// requireToken lets an admin (full operator) caller through.
func (s *Server) requireToken(next authenticatedHandler) http.HandlerFunc {
	return s.requireScope(tickets.ScopeAll, next)
}

// requireScope lets an authenticated operator or human user with at least
// wantScope through.
func (s *Server) requireScope(wantScope string, next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, status, err := s.authenticateRequest(r)
		if err != nil {
			writeError(w, status, err)
			return
		}
		if p.Kind == "token" {
			if !tickets.Allows(p.TokenRole, tickets.RoleOperator) {
				writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot do this; it needs a %s token", p.Name, p.TokenRole, tickets.RoleOperator))
				return
			}
			if !tickets.AllowsScope(p.TokenRole, p.TokenScope, wantScope) {
				need := wantScope
				if need == tickets.ScopeAll {
					need = "full"
				}
				writeError(w, http.StatusForbidden, fmt.Errorf("token %q has scope %q and cannot do this; it needs %s access", p.Name, p.TokenScope, need))
				return
			}
		} else {
			if !tickets.AllowsScope(tickets.RoleOperator, tickets.RoleToScope(p.Role), wantScope) {
				writeError(w, http.StatusForbidden, fmt.Errorf("role %q cannot perform this action", p.Role))
				return
			}
		}
		next(w, r.WithContext(withPrincipal(r.Context(), p)), p.Name)
	}
}

// requireRunner lets a runner's bearer token through. Session cookies are
// ignored on runner endpoints so human sessions can never act as runners.
func (s *Server) requireRunner(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errors.New("a bearer token is required"))
			return
		}
		auth, err := s.store.Authenticate(r.Context(), token)
		if errors.Is(err, tickets.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if auth.Role == tickets.RoleAll && publicAuthActive(s.state().config) {
			writeError(w, http.StatusForbidden, errors.New("legacy unscoped role-less tokens are disabled on public deployments; create a typed runner token"))
			return
		}
		if !tickets.Allows(auth.Role, tickets.RoleRunner) {
			writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot do this; it needs a %s token", auth.Name, auth.Role, tickets.RoleRunner))
			return
		}
		next(w, r, auth.Name)
	}
}

// csrfMiddleware enforces Go stdlib net/http.CrossOriginProtection (plus
// explicit Origin: null rejection) on all non-safe requests that are not
// strictly authenticated via Authorization: Bearer.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if hasBearerHeader(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Origin")), "null") {
			writeError(w, http.StatusForbidden, errors.New("cross-origin request blocked"))
			return
		}
		if err := s.csrf.Check(r); err != nil {
			writeError(w, http.StatusForbidden, errors.New("cross-origin request blocked"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	cfg := s.state().config
	https := isHTTPS(cfg, r)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(cfg, r),
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   https,
	})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	cfg := s.state().config
	ip := clientIP(r, cfg.Server.TrustedProxyHops)
	if !s.authLimiter.Allow(ip) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, errors.New("too many sign-in attempts; try again shortly"))
		return
	}
	if !cfg.Auth.TokenSigninEnabled() {
		writeError(w, http.StatusForbidden, errors.New("token sign-in is disabled on this instance"))
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	auth, err := s.store.Authenticate(r.Context(), request.Token)
	if errors.Is(err, tickets.ErrNotFound) {
		s.audit(r, "auth.login.denied", "", "", "token", "denied", slog.String("reason", "invalid_token"))
		writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if auth.Role == tickets.RoleAll && publicAuthActive(cfg) {
		s.audit(r, "auth.login.denied", auth.ID, auth.Name, "token", "denied", slog.String("reason", "legacy_role_all"))
		writeError(w, http.StatusForbidden, errors.New("legacy unscoped role-less tokens are disabled on public deployments; create a typed operator token"))
		return
	}
	if !tickets.Allows(auth.Role, tickets.RoleOperator) {
		s.audit(r, "auth.login.denied", auth.ID, auth.Name, "token", "denied", slog.String("reason", "runner_token"))
		writeError(w, http.StatusForbidden, errors.New("this is a runner's token; sign in with your own, from machinist token create <your-name>"))
		return
	}
	if !tickets.AllowsScope(auth.Role, auth.Scope, tickets.ScopeAll) {
		s.audit(r, "auth.login.denied", auth.ID, auth.Name, "token", "denied", slog.String("reason", "insufficient_scope"))
		writeError(w, http.StatusForbidden, fmt.Errorf("this token has scope %q and cannot sign in to the web app; sign in with your own, from machinist token create <your-name>", auth.Scope))
		return
	}
	secret, _, err := s.store.CreateTokenSession(r.Context(), auth.ID, r.UserAgent(), ip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setSessionCookie(w, r, secret, sessionMaxAge)
	s.audit(r, "auth.login.succeeded", auth.ID, auth.Name, auth.ID, "succeeded", slog.String("method", "token"))
	writeJSON(w, http.StatusOK, map[string]string{"name": auth.Name})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request, _ string) {
	if p, ok := principalFromRequest(r); ok && p.SessionID != "" {
		_ = s.store.RevokeSession(r.Context(), p.SessionID)
	}
	s.setSessionCookie(w, r, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}
