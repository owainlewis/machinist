package controlplane

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/owainlewis/machinist/internal/artifacts"
	"mime"
	"net/http"
	"path"
)

func (s *Server) authorizeArtifact(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			if !s.validBearerRequest(r) {
				writeUnauthorized(w)
				return
			}
		} else if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Machinist-CSRF")), []byte(s.csrfToken)) != 1 || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			writeError(w, 403, errors.New("artifact access requires a worker token or same-origin browser request"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}
func artifactHTTPError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, artifacts.ErrTooLarge):
		code = 413
	case errors.Is(err, ErrArtifactInvalid):
		code = 422
	case errors.Is(err, sql.ErrNoRows):
		code = 404
	case errors.Is(err, ErrArtifactExpired):
		code = 410
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, ErrRunState), errors.Is(err, ErrArtifactConflict):
		code = 409
	}
	writeError(w, code, err)
}
func (s *Server) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.store.storageConfig.MaxFileBytes+1)
	a, err := s.store.PublishArtifact(r.Context(), r.PathValue("id"), r.Header.Get("X-Machinist-Instance"), r.Header.Get("X-Machinist-Lease"), r.URL.Query().Get("path"), r.Body)
	if err != nil {
		artifactHTTPError(w, err)
		return
	}
	writeJSON(w, 201, a)
}
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.ListArtifacts(r.Context(), r.PathValue("id"))
	if err != nil {
		artifactHTTPError(w, err)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request) {
	a, f, err := s.store.OpenArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		artifactHTTPError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(a.Path)}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, a.Path, a.CreatedAt, f)
}
