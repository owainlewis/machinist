package factory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestLocalFolderPickerUsesNativeDirectoriesAndBounds(t *testing.T) {
	s, _ := fixture(t)
	root := t.TempDir()
	for _, name := range []string{"folder with spaces", ".hidden", "quote's directory"} {
		if e := os.Mkdir(filepath.Join(root, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, "file.txt"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	_ = os.Symlink(filepath.Join(root, "folder with spaces"), filepath.Join(root, "directory-link"))
	// Native local browsing works without sh, ls, or another executable.
	t.Setenv("PATH", t.TempDir())
	w := call(s, "GET", "folders?host=local&path="+url.QueryEscape(root), "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var listing folderListing
	if e := json.Unmarshal(w.Body.Bytes(), &listing); e != nil {
		t.Fatal(e)
	}
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	if listing.Path != canonical || listing.Parent != filepath.Dir(canonical) || len(listing.Folders) != 3 || listing.Truncated {
		t.Fatal("incorrect native directory listing", listing)
	}
	for _, entry := range listing.Folders {
		if entry.Path != filepath.Join(listing.Path, entry.Name) {
			t.Fatal("incorrect folder path", entry)
		}
	}
	for i := 0; i < 220; i++ {
		if e = os.Mkdir(filepath.Join(root, id("folder_")), 0700); e != nil {
			t.Fatal(e)
		}
	}
	w = call(s, "GET", "folders?path="+url.QueryEscape(root), "", "")
	if e = json.Unmarshal(w.Body.Bytes(), &listing); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || len(listing.Folders) != 200 || !listing.Truncated {
		t.Fatal("native folder limit missing", listing)
	}
}

func TestFolderPickerBrowserAuthorityAndInputValidation(t *testing.T) {
	s, _ := fixture(t)
	s.SetCSRFToken("browser-csrf")
	for _, tc := range []struct {
		endpoint, authorization, csrf, origin, site string
		code                                        int
	}{
		{"folders?path=/", "", "", "", "", 403},
		{"folders?path=/", "Bearer worker", "browser-csrf", "", "", 403},
		{"folders?path=/", "", "browser-csrf", "http://unrelated.invalid", "", 403},
		{"folders?path=/", "", "browser-csrf", "", "cross-site", 403},
		{"folders?host=unknown&path=/", "", "browser-csrf", "", "", 409},
		{"folders?path=relative", "", "browser-csrf", "", "", 409},
		{"folders?path=%2Ftmp%0Ainvalid", "", "browser-csrf", "", "", 409},
	} {
		request := httptest.NewRequest("GET", "/api/factory/"+tc.endpoint, nil)
		request.Header.Set("Authorization", tc.authorization)
		request.Header.Set("X-Machinist-CSRF", tc.csrf)
		request.Header.Set("Origin", tc.origin)
		request.Header.Set("Sec-Fetch-Site", tc.site)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Fatal(tc, response.Code, response.Body.String())
		}
	}
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	home, e = filepath.EvalSymlinks(home)
	if e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("GET", "/api/factory/folders?host=local", nil)
	request.Header.Set("X-Machinist-CSRF", "browser-csrf")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	var listing folderListing
	if e = json.Unmarshal(response.Body.Bytes(), &listing); e != nil || response.Code != 200 || listing.Path != home {
		t.Fatal("default home failed", e, response.Body.String())
	}
}

func TestRemoteFolderPickerUsesPinnedHostAndQuotedPOSIXPath(t *testing.T) {
	s, _ := fixture(t)
	root := t.TempDir()
	special := filepath.Join(root, "directory'; touch injected; #")
	if e := os.Mkdir(special, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(special, "child's folder"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(special, "file"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "host")
	script := "#!/bin/sh\n[ \"$1\" = '-o' ] && [ \"$2\" = 'BatchMode=yes' ] && [ \"$3\" = 'fixture-vm' ] || exit 9\nprintf '%s' \"$3\" > " + shellQuote(log) + "\ncd " + shellQuote(bin) + " || exit 9\nHOME=" + shellQuote(special) + " exec sh -c \"$4\"\n"
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.mu.Lock()
	s.cfg.Hosts["vm"] = config.FactoryHost{Name: "VM", SSH: "fixture-vm"}
	s.mu.Unlock()
	for _, selected := range []string{"", special} {
		w := call(s, "GET", "folders?host=vm&path="+url.QueryEscape(selected), "", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var listing folderListing
		if e := json.Unmarshal(w.Body.Bytes(), &listing); e != nil {
			t.Fatal(e)
		}
		canonical, _ := filepath.EvalSymlinks(special)
		if listing.Path != canonical || len(listing.Folders) != 1 || listing.Folders[0].Name != "child's folder" {
			t.Fatal("remote directory listing incorrect", listing)
		}
	}
	host, e := os.ReadFile(log)
	if e != nil || string(host) != "fixture-vm" {
		t.Fatal("wrong host selected", e, string(host))
	}
	if _, e = os.Stat(filepath.Join(bin, "injected")); !os.IsNotExist(e) {
		t.Fatal("remote path interpreted as shell code", e)
	}
	w := call(s, "GET", "folders?host=vm&path=C%3A%2Frepository", "", "")
	if w.Code != 409 {
		t.Fatal("remote path used controller drive semantics")
	}
}

func TestRemoteFolderOutputBoundAndIOOutsideMutex(t *testing.T) {
	s, _ := fixture(t)
	bin := t.TempDir()
	ready := filepath.Join(bin, "ready")
	release := filepath.Join(bin, "release")
	script := "#!/bin/sh\ntouch " + shellQuote(ready) + "\nwhile [ ! -e " + shellQuote(release) + " ]; do sleep 0.01; done\nprintf '/srv\\000'\nawk 'BEGIN {for(i=0;i<1000;i++){printf \"/srv/\";for(j=0;j<200;j++)printf \"x\";printf \"%d%c\",i,0}}'\n"
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.mu.Lock()
	s.cfg.Hosts["vm"] = config.FactoryHost{Name: "VM", SSH: "fixture-vm"}
	s.mu.Unlock()
	defer os.WriteFile(release, nil, 0600)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(s, "GET", "folders?host=vm&path=/srv", "", "") }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote listing did not start")
		}
		time.Sleep(time.Millisecond)
	}
	status := make(chan int, 1)
	go func() { status <- call(s, "GET", "status", "", "").Code }()
	select {
	case code := <-status:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH listing held service mutex")
	}
	if e := os.WriteFile(release, nil, 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case w := <-done:
		var listing folderListing
		if e := json.Unmarshal(w.Body.Bytes(), &listing); e != nil || w.Code != 200 || !listing.Truncated || len(listing.Folders) != 200 || w.Body.Len() > 128<<10 {
			t.Fatal("remote output not bounded", e, w.Code, w.Body.Len(), len(listing.Folders))
		}
	case <-time.After(time.Second):
		t.Fatal("remote listing did not finish")
	}
}

func TestStatusForemanMetadataUsesConfiguredRuntimeAndModel(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	profile := s.cfg.Agents[s.cfg.Foreman]
	profile.Name = "Factory foreman"
	profile.Model = "configured-model"
	profile.Runtime = "claude"
	s.cfg.Agents[s.cfg.Foreman] = profile
	s.mu.Unlock()
	w := call(s, "GET", "status", "", "")
	var response struct {
		Foreman struct{ ID, Name, Runtime, Model string }
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if response.Foreman.ID != "foreman" || response.Foreman.Name != "Factory foreman" || response.Foreman.Runtime != "claude" || response.Foreman.Model != "configured-model" {
		t.Fatal("configured foreman metadata missing", response)
	}
	s.mu.Lock()
	profile.Model = ""
	profile.Runtime = ""
	s.cfg.Agents[s.cfg.Foreman] = profile
	s.mu.Unlock()
	w = call(s, "GET", "status", "", "")
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if response.Foreman.Runtime != "claude" || response.Foreman.Model != "" {
		t.Fatal("default model was invented", response)
	}
	// Cancellation is accepted before any folder IO starts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := localFolders(ctx, t.TempDir())
	if e == nil || !strings.Contains(e.Error(), "canceled") {
		t.Fatal("folder read ignored cancellation", e)
	}
}
