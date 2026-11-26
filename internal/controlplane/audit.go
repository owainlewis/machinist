package controlplane

import (
	"io"
	"log/slog"
	"net/http"
)

// auditLogger emits single-line structured JSON audit records to the server's
// log writer (stdout on Cloud Run / journald on VMs) using Go stdlib log/slog.
type auditLogger struct {
	logger *slog.Logger
}

func newAuditLogger(w io.Writer) *auditLogger {
	if w == nil {
		w = io.Discard
	}
	return &auditLogger{
		logger: slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
}

func (s *Server) audit(r *http.Request, action, actorID, actorName, target, outcome string, extra ...slog.Attr) {
	if s == nil || s.auditLog == nil {
		return
	}
	var ip string
	if r != nil {
		ip = clientIP(r, s.state().config.Server.TrustedProxyHops)
	}
	attrs := make([]any, 0, 14+len(extra)*2)
	attrs = append(attrs,
		slog.Bool("audit", true),
		slog.String("action", action),
		slog.String("actor_id", actorID),
		slog.String("actor_name", actorName),
		slog.String("target", target),
		slog.String("outcome", outcome),
		slog.String("ip", ip),
	)
	for _, attr := range extra {
		attrs = append(attrs, attr)
	}
	s.auditLog.logger.Info("audit", attrs...)
}
