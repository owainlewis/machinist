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

// requestToken reads the bearer token, or otherwise the sign-in cookie.
func requestToken(r *http.Request) string {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return token
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// requireToken lets a person's token through: an operator token, or one from
// before roles.
func (s *Server) requireToken(next authenticatedHandler) http.HandlerFunc {
	return s.requireRole(tickets.RoleOperator, next)
}

// requireRunner lets a runner's token through.
func (s *Server) requireRunner(next authenticatedHandler) http.HandlerFunc {
	return s.requireRole(tickets.RoleRunner, next)
}

func (s *Server) requireRole(want string, next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errors.New("a token is required"))
			return
		}
		name, role, err := s.store.TokenName(r.Context(), token)
		if errors.Is(err, tickets.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !tickets.Allows(role, want) {
			writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot do this; it needs a %s token", name, role, want))
			return
		}
		next(w, r, name)
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
		Secure:   s.config.Server.TLSCert != "" || (r != nil && r.TLS != nil),
	})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	name, role, err := s.store.TokenName(r.Context(), request.Token)
	if err == nil && !tickets.Allows(role, tickets.RoleOperator) {
		writeError(w, http.StatusForbidden, errors.New("this is a runner's token; sign in with your own, from machinist token create <your-name>"))
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
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request, _ string) {
	s.setSessionCookie(w, r, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}
