package sources

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTokens is Linear's OAuth token endpoint. Each grant hands out a new token.
type fakeTokens struct {
	mu        sync.Mutex
	grants    int
	expiresIn int
	forms     []map[string]string
	status    int
}

func (f *fakeTokens) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	form := map[string]string{"content-type": r.Header.Get("Content-Type")}
	for key := range r.PostForm {
		form[key] = r.PostForm.Get(key)
	}
	f.forms = append(f.forms, form)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client authentication failed"}`))
		return
	}
	f.grants++
	_, _ = fmt.Fprintf(w, `{"access_token":"lin_oauth_%d","token_type":"Bearer","expires_in":%d,"scope":"read write"}`, f.grants, f.expiresIn)
}

func (f *fakeTokens) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants
}

// credentialPolling is a poll against fake Linear, with its token from the
// client credentials.
func credentialPolling(t *testing.T) (*polling, *fakeTokens, *LinearCredentials, *time.Time) {
	t.Helper()
	p := newPolling(t)
	tokens := &fakeTokens{expiresIn: 2591999}
	server := httptest.NewServer(tokens)
	t.Cleanup(server.Close)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	credentials := &LinearCredentials{ClientID: "client-id", ClientSecret: "client-secret", Endpoint: server.URL, Client: server.Client(), now: func() time.Time { return now }}
	linear := p.poller.Tracker.(*Linear)
	linear.Token = ""
	linear.Credentials = credentials
	return p, tokens, credentials, &now
}

func TestTheClientCredentialsGetAToken(t *testing.T) {
	p, tokens, _, _ := credentialPolling(t)
	p.poll(t)
	if tokens.count() != 1 {
		t.Fatalf("grants = %d", tokens.count())
	}
	want := map[string]string{
		"content-type":  "application/x-www-form-urlencoded",
		"grant_type":    "client_credentials",
		"client_id":     "client-id",
		"client_secret": "client-secret",
		"scope":         "read,write",
	}
	if got := tokens.forms[0]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("form = %v", got)
	}
	if p.linear.auth != "Bearer lin_oauth_1" {
		t.Fatalf("authorization = %q", p.linear.auth)
	}
	p.poll(t)
	if tokens.count() != 1 || p.linear.auth != "Bearer lin_oauth_1" {
		t.Fatalf("a second poll fetched again: grants = %d, authorization = %q", tokens.count(), p.linear.auth)
	}
}

func TestATokenNearExpiryIsRenewedFirst(t *testing.T) {
	p, tokens, _, now := credentialPolling(t)
	p.poll(t)
	// 2591999 seconds is just under 30 days. With more than an hour left, the
	// token is kept.
	*now = now.Add(29*24*time.Hour + 22*time.Hour)
	p.poll(t)
	if tokens.count() != 1 {
		t.Fatalf("renewed too early: grants = %d", tokens.count())
	}
	*now = now.Add(time.Hour)
	p.poll(t)
	if tokens.count() != 2 || p.linear.auth != "Bearer lin_oauth_2" {
		t.Fatalf("grants = %d, authorization = %q", tokens.count(), p.linear.auth)
	}
}

func TestARejectedTokenIsRenewedOnceAndTheCallRetried(t *testing.T) {
	p, tokens, _, _ := credentialPolling(t)
	p.poll(t)
	p.linear.mu.Lock()
	p.linear.revoked = map[string]bool{"Bearer lin_oauth_1": true}
	asked := len(p.linear.queries)
	p.linear.mu.Unlock()
	p.poll(t)
	if tokens.count() != 2 || p.linear.auth != "Bearer lin_oauth_2" {
		t.Fatalf("grants = %d, authorization = %q", tokens.count(), p.linear.auth)
	}
	// The rejected query was sent again with the new token.
	if p.linear.queries[asked] != p.linear.queries[asked+1] {
		t.Fatalf("the rejected query was not retried: %q", p.linear.queries[asked:])
	}
}

func TestARenewedTokenThatIsRejectedTooIsNotRenewedAgain(t *testing.T) {
	p, tokens, _, _ := credentialPolling(t)
	p.linear.revoked = map[string]bool{"Bearer lin_oauth_1": true, "Bearer lin_oauth_2": true}
	err := p.poller.Poll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v", err)
	}
	if tokens.count() != 2 {
		t.Fatalf("grants = %d, want one fetch and one renewal", tokens.count())
	}
}

func TestBadClientCredentialsSayWhy(t *testing.T) {
	p, tokens, _, _ := credentialPolling(t)
	tokens.status = http.StatusUnauthorized
	err := p.poller.Poll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Client authentication failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestAFixedTokenIsNotRenewedOnARejection(t *testing.T) {
	p := newPolling(t)
	p.linear.revoked = map[string]bool{"Bearer lin_oauth_token": true}
	if err := p.poller.Poll(t.Context()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v", err)
	}
}
