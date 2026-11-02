package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

const maxRunEventSearchResults = 128

// searchRunEvents returns the events for a run whose body contains the query text.
func (s *GitHubFactoryServer) searchRunEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, 400, errors.New("query is required"))
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > maxRunEventSearchResults {
		limit = maxRunEventSearchResults
	}
	rows, err := s.local.db.QueryContext(r.Context(), `SELECT sequence,body FROM run_activity_events WHERE run=? AND body LIKE ? ESCAPE '\' ORDER BY sequence LIMIT ?`, r.PathValue("id"), "%"+escapeLike(q)+"%", limit)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	page := protocol.RunEventPage{Events: []json.RawMessage{}}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&page.Cursor, &raw); err != nil {
			writeError(w, 500, err)
			return
		}
		page.Events = append(page.Events, json.RawMessage(raw))
	}
	if err = rows.Err(); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, page)
}

// escapeLike makes LIKE treat % and _ in user text as literal characters.
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}
