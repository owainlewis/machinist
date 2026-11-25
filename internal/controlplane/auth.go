package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	sessionCookie = "machinist_session"
	sessionMaxAge = 30 * 24 * time.Hour
)

type authenticatedHandler func(w http.ResponseWriter, r *http.Request, tokenName string)

// bearerToken reads the bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}

// requestToken reads the bearer token, or otherwise the sign-in cookie.
func requestToken(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// requireToken lets a full operator token through: an unscoped operator token,
// or one from before roles.
func (s *Server) requireToken(next authenticatedHandler) http.HandlerFunc {
	return s.requireScope(tickets.ScopeAll, next)
}

// requireScope lets an operator token with at least wantScope through.
func (s *Server) requireScope(wantScope string, next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errors.New("a token is required"))
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
		if !tickets.Allows(auth.Role, tickets.RoleOperator) {
			writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot do this; it needs a %s token", auth.Name, auth.Role, tickets.RoleOperator))
			return
		}
		if !tickets.AllowsScope(auth.Role, auth.Scope, wantScope) {
			need := wantScope
			if need == tickets.ScopeAll {
				need = "full"
			}
			writeError(w, http.StatusForbidden, fmt.Errorf("token %q has scope %q and cannot do this; it needs %s access", auth.Name, auth.Scope, need))
			return
		}
		next(w, r, auth.Name)
	}
}

// requireRunner lets a runner's token through.
func (s *Server) requireRunner(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errors.New("a token is required"))
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
		if !tickets.Allows(auth.Role, tickets.RoleRunner) {
			writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot do this; it needs a %s token", auth.Name, auth.Role, tickets.RoleRunner))
			return
		}
		next(w, r, auth.Name)
	}
}

// The cookie holds the token itself, so revoking the token ends the sign-in.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.state().config.Server.TLSCert != "" || (r != nil && r.TLS != nil),
	})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	auth, err := s.store.Authenticate(r.Context(), request.Token)
	if err == nil && !tickets.Allows(auth.Role, tickets.RoleOperator) {
		writeError(w, http.StatusForbidden, errors.New("this is a runner's token; sign in with your own, from machinist token create <your-name>"))
		return
	}
	if err == nil && !tickets.AllowsScope(auth.Role, auth.Scope, tickets.ScopeAll) {
		writeError(w, http.StatusForbidden, fmt.Errorf("this token has scope %q and cannot sign in to the web app; sign in with your own, from machinist token create <your-name>", auth.Scope))
		return
	}
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setSessionCookie(w, r, request.Token, sessionMaxAge)
	writeJSON(w, http.StatusOK, map[string]string{"name": auth.Name})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request, _ string) {
	s.setSessionCookie(w, r, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}
