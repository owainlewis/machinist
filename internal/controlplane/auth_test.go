package controlplane

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
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
		{http.MethodGet, "/api/v1/runs/run_x/patch"},
		{http.MethodPost, "/api/v1/tickets"},
		{http.MethodGet, "/api/v1/tickets"},
		{http.MethodGet, "/api/v1/tickets/t_x"},
		{http.MethodPost, "/api/v1/tickets/t_x/cancel"},
		{http.MethodGet, "/api/v1/ledger"},
		{http.MethodPost, "/machinist/run"},
		{http.MethodGet, "/machinist/jobs"},
		{http.MethodGet, "/machinist/jobs/t_x"},
		{http.MethodPost, "/machinist/jobs/t_x/cancel"},
		{http.MethodDelete, "/api/v1/session"},
		{http.MethodGet, "/api/v1/repositories"},
		{http.MethodGet, "/api/v1/automations"},
		{http.MethodPost, "/api/v1/automations/x/run"},
		{http.MethodPost, "/mcp"},
	} {
		for _, token := range []string{"", "wrong"} {
			response, body := f.call(t, route.method, route.path, token, protocol.PollRequest{})
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s with %q = %d: %s", route.method, route.path, token, response.StatusCode, body)
			}
		}
	}
}

func (f *fixture) signInCookie(t *testing.T, token string) *http.Cookie {
	t.Helper()
	response, body := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": token})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sign in failed: %d: %s", response.StatusCode, body)
	}
	cookie := findCookie(response)
	if cookie == nil {
		t.Fatalf("sign in returned no session cookie: %v", response.Cookies())
	}
	return cookie
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
		if cookie.Name == sessionCookie || cookie.Name == secureSessionCookie {
			return cookie
		}
	}
	return nil
}

func TestSignInSetsAnOpaqueSessionCookie(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": f.token})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "runner-1") {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	cookie := findCookie(response)
	if cookie == nil || cookie.Name != sessionCookie || !strings.HasPrefix(cookie.Value, tickets.SessionPrefix) || cookie.Value == f.token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure || cookie.Path != "/" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", cookie.Value); got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", got.StatusCode)
	}
	// Stuffing the raw bearer token directly into the session cookie must not work.
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", f.token); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw token in cookie = %d, want 401", got.StatusCode)
	}
}

func TestSignInCookieIsSecureUnderTLS(t *testing.T) {
	f := newFixture(t)
	f.server.config.Server.TLSCert = "cert.pem"
	response, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": f.token})
	if cookie := findCookie(response); cookie == nil || !cookie.Secure || cookie.Name != secureSessionCookie {
		t.Fatalf("cookie = %+v, want Secure __Host- cookie", cookie)
	}
}

func TestSessionCookieIsSecureWhenRequestUsesTLSWithoutConfigTLSCert(t *testing.T) {
	f := newFixture(t)
	tlsServer := httptest.NewTLSServer(f.server.Handler())
	t.Cleanup(tlsServer.Close)

	req, err := http.NewRequest(http.MethodPost, tlsServer.URL+"/api/v1/session", bytes.NewBufferString(`{"token":"`+f.token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := tlsServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sign-in status = %d", response.StatusCode)
	}
	cookie := findCookie(response)
	if cookie == nil || !cookie.Secure || cookie.Name != secureSessionCookie {
		t.Fatalf("sign-in cookie over TLS = %+v, want Secure __Host- cookie", cookie)
	}

	signOutReq, err := http.NewRequest(http.MethodDelete, tlsServer.URL+"/api/v1/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	signOutReq.AddCookie(&http.Cookie{Name: secureSessionCookie, Value: cookie.Value})
	signOutResp, err := tlsServer.Client().Do(signOutReq)
	if err != nil {
		t.Fatal(err)
	}
	signOutResp.Body.Close()
	if signOutResp.StatusCode != http.StatusNoContent {
		t.Fatalf("sign-out status = %d", signOutResp.StatusCode)
	}
	cleared := findCookie(signOutResp)
	if cleared == nil || !cleared.Secure || cleared.Name != secureSessionCookie || cleared.MaxAge >= 0 {
		t.Fatalf("sign-out cookie over TLS = %+v, want Secure __Host- cookie and cleared", cleared)
	}
}

func TestSignInRejectsABadToken(t *testing.T) {
	f := newFixture(t)
	response, _ := f.call(t, http.MethodPost, "/api/v1/session", "", map[string]string{"token": "wrong"})
	if response.StatusCode != http.StatusUnauthorized || findCookie(response) != nil {
		t.Fatalf("status = %d, cookies = %v", response.StatusCode, response.Cookies())
	}
}

func TestSignOutClearsAndRevokesTheSession(t *testing.T) {
	f := newFixture(t)
	signedIn := f.signInCookie(t, f.token)
	response := f.withCookie(t, http.MethodDelete, "/api/v1/session", signedIn.Value)
	cookie := findCookie(response)
	if response.StatusCode != http.StatusNoContent || cookie == nil || cookie.Value != "" || cookie.MaxAge >= 0 {
		t.Fatalf("status = %d, cookie = %+v", response.StatusCode, cookie)
	}
	// Reusing the revoked session cookie must fail.
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", signedIn.Value); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session still worked: %d", got.StatusCode)
	}
}

func TestRevokingATokenEndsItsSignIn(t *testing.T) {
	f := newFixture(t)
	signedIn := f.signInCookie(t, f.token)
	if err := f.store.RevokeToken(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tickets", signedIn.Value); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", got.StatusCode)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tickets", f.token, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
