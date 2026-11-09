package controlplane

import (
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestRunnerRoutesRequireAValidToken(t *testing.T) {
	f := newFixture(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/runners/poll"},
		{http.MethodGet, "/api/v1/runners"},
		{http.MethodPost, "/api/v1/runs/run_x/heartbeat"},
		{http.MethodPost, "/api/v1/runs/run_x/events"},
		{http.MethodPost, "/api/v1/runs/run_x/complete"},
	} {
		for _, token := range []string{"", "wrong"} {
			response, body := f.call(t, route.method, route.path, token, protocol.PollRequest{})
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s with %q = %d: %s", route.method, route.path, token, response.StatusCode, body)
			}
		}
	}
}
