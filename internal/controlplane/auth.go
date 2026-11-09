package controlplane

import (
	"errors"
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

func (s *Server) requireToken(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errors.New("a token is required"))
			return
		}
		name, err := s.store.TokenName(r.Context(), token)
		if errors.Is(err, tickets.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		next(w, r, name)
	}
}

// The cookie holds the token itself, so revoking the token ends the sign-in.
func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.config.Server.TLSCert != "",
	})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	name, err := s.store.TokenName(r.Context(), request.Token)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.setSessionCookie(w, request.Token, sessionMaxAge)
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) signOut(w http.ResponseWriter, _ *http.Request, _ string) {
	s.setSessionCookie(w, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}
