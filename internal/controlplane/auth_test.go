package controlplane

import (
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestEveryRouteRequiresAValidToken(t *testing.T) {
	f := newFixture(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/runners/poll"},
		{http.MethodGet, "/api/v1/runners"},
		{http.MethodPost, "/api/v1/runs/run_x/heartbeat"},
		{http.MethodPost, "/api/v1/runs/run_x/events"},
		{http.MethodPost, "/api/v1/runs/run_x/complete"},
		{http.MethodGet, "/api/v1/runs/run_x/events"},
		{http.MethodPost, "/api/v1/tickets"},
		{http.MethodGet, "/api/v1/tickets"},
		{http.MethodGet, "/api/v1/tickets/t_x"},
		{http.MethodPost, "/api/v1/tickets/t_x/answer"},
		{http.MethodPost, "/api/v1/tickets/t_x/cancel"},
		{http.MethodGet, "/api/v1/ledger"},
		{http.MethodDelete, "/api/v1/session"},
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/mcp"},
	} {
		for _, token := range []string{"", "wrong"} {
			response, body := f.call(t, route.method, route.path, token, protocol.PollRequest{})
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s with %q = %d: %s", route.method, route.path, token, response.StatusCode, body)
			}
		}
	}
}

func (f *fixture) withCookie(t *testing.T, method, path, value string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, f.http.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response
}

func findCookie(response *http.Response) *http.Cookie {
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	return nil
}

func TestSignInSetsAStrictCookie(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": f.token})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "runner-1") {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	cookie := findCookie(response)
	if cookie == nil || cookie.Value != f.token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", cookie.Value); got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", got.StatusCode)
	}
}

func TestSignInCookieIsSecureUnderTLS(t *testing.T) {
	f := newFixture(t)
	f.server.config.Server.TLSCert = "cert.pem"
	response, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": f.token})
	if cookie := findCookie(response); cookie == nil || !cookie.Secure {
		t.Fatalf("cookie = %+v", cookie)
	}
}

func TestSignInRejectsABadToken(t *testing.T) {
	f := newFixture(t)
	response, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": "wrong"})
	if response.StatusCode != http.StatusUnauthorized || findCookie(response) != nil {
		t.Fatalf("status = %d, cookies = %v", response.StatusCode, response.Cookies())
	}
}

func TestSignOutClearsTheCookie(t *testing.T) {
	f := newFixture(t)
	response := f.withCookie(t, http.MethodDelete, "/api/v1/session", f.token)
	cookie := findCookie(response)
	if response.StatusCode != http.StatusNoContent || cookie == nil || cookie.Value != "" || cookie.MaxAge >= 0 {
		t.Fatalf("status = %d, cookie = %+v", response.StatusCode, cookie)
	}
}

func TestRevokingATokenEndsItsSignIn(t *testing.T) {
	f := newFixture(t)
	if err := f.store.RevokeToken(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", f.token); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", got.StatusCode)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tickets", f.token, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
