package controlplane

import (
	"errors"
	"net/http"
	"strings"

	"github.com/owainlewis/machinist/internal/tickets"
)

type authenticatedHandler func(w http.ResponseWriter, r *http.Request, tokenName string)

func (s *Server) requireToken(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
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
