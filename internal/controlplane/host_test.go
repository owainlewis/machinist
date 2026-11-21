package controlplane

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

func handlerListeningOn(t *testing.T, listen string) http.Handler {
	t.Helper()
	fakegh.Install(t, testConfig().GitHub.Handle)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	loaded := testConfig()
	loaded.Server.Listen = listen
	server, err := New(loaded, store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

func serveWithHost(handler http.Handler, path, host string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Host = host
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestLoopbackListenAcceptsLoopbackHosts(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:7331", "[::1]:7331", "localhost:7331"} {
		handler := handlerListeningOn(t, listen)
		for _, host := range []string{"localhost", "localhost:7331", "127.0.0.1", "127.0.0.1:7331", "[::1]", "[::1]:7331"} {
			for _, path := range []string{"/healthz", "/"} {
				if response := serveWithHost(handler, path, host); response.Code != http.StatusOK {
					t.Errorf("listen %s, Host %q, %s = %d: %s", listen, host, path, response.Code, response.Body)
				}
			}
		}
	}
}

func TestLoopbackListenRejectsOtherHosts(t *testing.T) {
	handler := handlerListeningOn(t, "127.0.0.1:7331")
	hosts := []string{
		"evil.example",
		"evil.example:7331",
		"",
		"localhost:",
		"localhost:port",
		"localhost:+80",
		"localhost:70000",
		"[::1",
		"::1",
		"localhost.evil.example",
		"127.0.0.2",
	}
	for _, host := range hosts {
		for _, path := range []string{"/healthz", "/api/v1/tickets", "/"} {
			response := serveWithHost(handler, path, host)
			if response.Code != http.StatusMisdirectedRequest {
				t.Errorf("Host %q, %s = %d, want 421", host, path, response.Code)
				continue
			}
			if !strings.Contains(response.Body.String(), "misdirected request") {
				t.Errorf("Host %q, %s body = %q", host, path, response.Body)
			}
		}
	}
}

func TestNonLoopbackListenServesAnyHost(t *testing.T) {
	for _, listen := range []string{"10.0.0.5:7331", "0.0.0.0:7331", ":7331"} {
		handler := handlerListeningOn(t, listen)
		for _, host := range []string{"evil.example", "10.0.0.5:7331", "machinist.internal"} {
			if response := serveWithHost(handler, "/healthz", host); response.Code != http.StatusOK {
				t.Errorf("listen %s, Host %q = %d: %s", listen, host, response.Code, response.Body)
			}
		}
	}
}

func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7331":  true,
		"127.0.0.5:7331":  true,
		"[::1]:7331":      true,
		"localhost:7331":  true,
		"LOCALHOST:7331":  true,
		"10.0.0.5:7331":   false,
		"0.0.0.0:7331":    false,
		"[::]:7331":       false,
		":7331":           false,
		"example.com:443": false,
	}
	for listen, want := range cases {
		if got := isLoopbackListen(listen); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", listen, got, want)
		}
	}
}
