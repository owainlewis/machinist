package githubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// fakeGitHub checks each request's JWT and answers like the GitHub API.
type fakeGitHub struct {
	t             *testing.T
	key           *rsa.PublicKey
	installed     map[string]int64
	lookups       atomic.Int32
	tokenRequests []map[string]any
	tokenStatus   int
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.checkJWT(r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/installation"):
		f.lookups.Add(1)
		repository := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/installation")
		id, ok := f.installed[repository]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("token body: %v", err)
		}
		f.tokenRequests = append(f.tokenRequests, body)
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_scoped","expires_at":"2030-01-01T00:00:00Z"}`))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func (f *fakeGitHub) checkJWT(header string) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	parts := strings.Split(token, ".")
	if !ok || len(parts) != 3 {
		f.t.Errorf("authorization = %q", header)
		return
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.key, crypto.SHA256, digest[:], signature); err != nil {
		f.t.Errorf("JWT signature: %v", err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}
	_ = json.Unmarshal(payload, &claims)
	if claims.ISS != "1234" || claims.EXP-claims.IAT > 10*60 || claims.EXP <= claims.IAT {
		f.t.Errorf("claims = %+v", claims)
	}
}

func newApp(t *testing.T) (*App, *fakeGitHub) {
	key := testKey(t)
	fake := &fakeGitHub{t: t, key: &key.PublicKey, installed: map[string]int64{"owner/repo": 42, "owner/other": 42}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return New(1234, key).WithBaseURL(server.URL, server.Client()), fake
}

func TestTokenIsScopedToTheRunsRepository(t *testing.T) {
	app, fake := newApp(t)
	token, err := app.Token(t.Context(), "owner/repo")
	if err != nil || token != "ghs_scoped" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	body := fake.tokenRequests[0]
	if repositories, _ := json.Marshal(body["repositories"]); string(repositories) != `["repo"]` {
		t.Fatalf("repositories = %s", repositories)
	}
	permissions, _ := body["permissions"].(map[string]any)
	if len(permissions) != len(Permissions) || permissions["contents"] != "write" || permissions["checks"] != "read" || permissions["workflows"] != nil || permissions["administration"] != nil {
		t.Fatalf("permissions = %v", permissions)
	}
}

func TestTokenRemembersTheInstallation(t *testing.T) {
	app, fake := newApp(t)
	for range 3 {
		if _, err := app.Token(t.Context(), "owner/repo"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.lookups.Load() != 1 || len(fake.tokenRequests) != 3 {
		t.Fatalf("lookups = %d, tokens = %d", fake.lookups.Load(), len(fake.tokenRequests))
	}
}

func TestTokenNamesAMissingInstallation(t *testing.T) {
	app, _ := newApp(t)
	if _, err := app.Token(t.Context(), "owner/elsewhere"); err == nil || !strings.Contains(err.Error(), "not installed on owner/elsewhere") {
		t.Fatalf("error = %v", err)
	}
}

func TestTokenForgetsAnInstallationGitHubRefuses(t *testing.T) {
	app, fake := newApp(t)
	fake.tokenStatus = http.StatusUnprocessableEntity
	if _, err := app.Token(t.Context(), "owner/repo"); err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("error = %v", err)
	}
	fake.tokenStatus = 0
	if _, err := app.Token(t.Context(), "owner/repo"); err != nil {
		t.Fatal(err)
	}
	if fake.lookups.Load() != 2 {
		t.Fatalf("lookups = %d", fake.lookups.Load())
	}
}

func TestTokenRejectsARepositoryThatIsNotOwnerSlashName(t *testing.T) {
	app, _ := newApp(t)
	for _, repository := range []string{"", "owner", "owner/repo/extra", "../repo", "owner/..", "owner/repo?x=1"} {
		if _, err := app.Token(t.Context(), repository); err == nil || !strings.Contains(err.Error(), "invalid repository") {
			t.Fatalf("%q: error = %v", repository, err)
		}
	}
}

func TestLoadReadsPKCS1AndPKCS8Keys(t *testing.T) {
	key := testKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"pkcs1": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
		"pkcs8": {Type: "PRIVATE KEY", Bytes: pkcs8},
	} {
		path := filepath.Join(t.TempDir(), name+".pem")
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(1, path); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestLoadRejectsAFileThatIsNotAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(1, path); err == nil || !strings.Contains(err.Error(), "not a PEM file") {
		t.Fatalf("error = %v", err)
	}
	if _, err := Load(1, filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("a missing key file loaded")
	}
}

func TestCachedTokenIsReusedUntilItNearlyExpires(t *testing.T) {
	app, fake := newApp(t)
	for range 2 {
		if token, err := app.CachedToken(t.Context(), "owner/repo"); err != nil || token != "ghs_scoped" {
			t.Fatalf("token = %q, err = %v", token, err)
		}
	}
	if len(fake.tokenRequests) != 1 {
		t.Fatalf("token requests = %d", len(fake.tokenRequests))
	}
	// The fake's tokens expire at 2030-01-01. Five minutes before is too close.
	app.now = func() time.Time { return time.Date(2029, 12, 31, 23, 55, 0, 0, time.UTC) }
	if _, err := app.CachedToken(t.Context(), "owner/repo"); err != nil {
		t.Fatal(err)
	}
	if len(fake.tokenRequests) != 2 {
		t.Fatalf("token requests = %d", len(fake.tokenRequests))
	}
}

func TestTokenIsAlwaysNew(t *testing.T) {
	app, fake := newApp(t)
	for range 2 {
		if _, err := app.Token(t.Context(), "owner/repo"); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.tokenRequests) != 2 {
		t.Fatalf("token requests = %d", len(fake.tokenRequests))
	}
}
