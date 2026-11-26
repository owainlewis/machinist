package controlplane

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/owainlewis/machinist/internal/tickets"
)

func (s *Server) actorInfo(r *http.Request, fallbackName string) (id, name string) {
	if p, ok := principalFromRequest(r); ok {
		if p.UserID != "" {
			return p.UserID, p.Name
		}
		return p.TokenID, p.Name
	}
	return "", fallbackName
}

func (s *Server) buildInviteURL(secret string) string {
	path := "/#/invite#" + secret
	if pub := strings.TrimRight(s.state().config.Server.PublicURL, "/"); pub != "" {
		return pub + path
	}
	return path
}

func (s *Server) listTeamMembers(w http.ResponseWriter, r *http.Request, _ string) {
	members, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) updateTeamMember(w http.ResponseWriter, r *http.Request, tokenName string) {
	id := r.PathValue("id")
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &req) {
		return
	}
	if req.Role == nil && req.Disabled == nil {
		writeError(w, http.StatusBadRequest, errors.New("specify role or disabled"))
		return
	}
	if req.Role != nil && !tickets.ValidUserRole(*req.Role) {
		writeError(w, http.StatusBadRequest, errors.New("role must be admin, member, or viewer"))
		return
	}

	updated, err := s.store.UpdateUser(r.Context(), id, req.Role, req.Disabled)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("member not found"))
		return
	}
	if errors.Is(err, tickets.ErrLastAdmin) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	actorID, actorName := s.actorInfo(r, tokenName)
	if req.Role != nil {
		s.audit(r, "member.role_changed", actorID, actorName, updated.ID, "succeeded", slog.String("role", updated.Role))
	}
	if req.Disabled != nil {
		if *req.Disabled {
			s.audit(r, "member.disabled", actorID, actorName, updated.ID, "succeeded")
		} else {
			s.audit(r, "member.enabled", actorID, actorName, updated.ID, "succeeded")
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listTeamInvites(w http.ResponseWriter, r *http.Request, _ string) {
	invites, err := s.store.ListPendingInvites(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}

func (s *Server) createTeamInvite(w http.ResponseWriter, r *http.Request, tokenName string) {
	var req struct {
		Role        string `json:"role"`
		GitHubLogin string `json:"github_login"`
		Email       string `json:"email"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &req) {
		return
	}
	actorID, actorName := s.actorInfo(r, tokenName)
	createdBy := actorID
	if createdBy == "" {
		createdBy = actorName
	}

	inv, secret, err := s.store.CreateInvite(r.Context(), strings.TrimSpace(req.Role), req.GitHubLogin, req.Email, createdBy)
	if errors.Is(err, tickets.ErrInviteConflict) {
		writeError(w, http.StatusConflict, errors.New("a member matching this target already exists; update or re-enable them in Settings → Team"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	target := "github:" + inv.GitHubLogin
	if inv.GitHubLogin == "" {
		target = inv.Email
	}
	s.audit(r, "invite.created", actorID, actorName, inv.ID, "succeeded", slog.String("role", inv.Role), slog.String("invite_target", target))

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite":     inv,
		"invite_url": s.buildInviteURL(secret),
	})
}

func (s *Server) regenerateTeamInvite(w http.ResponseWriter, r *http.Request, tokenName string) {
	id := r.PathValue("id")
	inv, secret, err := s.store.RegenerateInvite(r.Context(), id)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("invite not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	actorID, actorName := s.actorInfo(r, tokenName)
	s.audit(r, "invite.regenerated", actorID, actorName, inv.ID, "succeeded")

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"invite":     inv,
		"invite_url": s.buildInviteURL(secret),
	})
}

func (s *Server) revokeTeamInvite(w http.ResponseWriter, r *http.Request, tokenName string) {
	id := r.PathValue("id")
	if err := s.store.RevokeInvite(r.Context(), id); errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("invite not found"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	actorID, actorName := s.actorInfo(r, tokenName)
	s.audit(r, "invite.revoked", actorID, actorName, id, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}
