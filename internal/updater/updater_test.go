package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateInstallsLatestVerifiedRelease(t *testing.T) {
	binary := []byte("new machinist")
	archive := testArchive(t, binary)
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/releases/latest":
			fmt.Fprint(response, `{"tag_name":"v1.2.3"}`)
		case "/downloads/v1.2.3/checksums.txt":
			fmt.Fprintf(response, "%x  machinist_1.2.3_linux_amd64.tar.gz\n", digest)
		case "/downloads/v1.2.3/machinist_1.2.3_linux_amd64.tar.gz":
			response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "machinist")
	if err := os.WriteFile(executable, []byte("old machinist"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Update(t.Context(), Options{
		Current:     "v1.0.0",
		Executable:  executable,
		GOOS:        "linux",
		GOARCH:      "amd64",
		Client:      server.Client(),
		APIBase:     server.URL + "/api",
		ReleaseBase: server.URL + "/downloads",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "v1.2.3" || result.AlreadyCurrent {
		t.Fatalf("result = %#v", result)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("installed binary = %q", got)
	}
}

func TestUpdateChecksumFailureLeavesExecutableUntouched(t *testing.T) {
	archive := testArchive(t, []byte("new machinist"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/checksums.txt"):
			fmt.Fprintln(response, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  machinist_1.2.3_linux_amd64.tar.gz")
		case strings.HasSuffix(request.URL.Path, ".tar.gz"):
			response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "machinist")
	original := []byte("old machinist")
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Update(t.Context(), Options{
		Version:     "v1.2.3",
		Current:     "v1.0.0",
		Executable:  executable,
		GOOS:        "linux",
		GOARCH:      "amd64",
		Client:      server.Client(),
		ReleaseBase: server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("executable changed to %q", got)
	}
}

func TestUpdateRejectsInvalidVersionBeforeNetworkAccess(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "machinist")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Update(t.Context(), Options{Version: "latest", Executable: executable})
	if err == nil || !strings.Contains(err.Error(), "expected vMAJOR.MINOR.PATCH") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateReportsCurrentVersionWithoutDownloadingAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/releases/latest" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		fmt.Fprint(response, `{"tag_name":"v1.2.3"}`)
	}))
	defer server.Close()
	result, err := Update(t.Context(), Options{Current: "v1.2.3", Client: server.Client(), APIBase: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyCurrent || result.Version != "v1.2.3" {
		t.Fatalf("result = %#v", result)
	}
}

func TestUpdateInstallsLatestNightlyRelease(t *testing.T) {
	const nightlyTag = "v0.5.1-nightly.20261124.abc1234"
	binary := []byte("nightly machinist")
	archive := testArchive(t, binary)
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/releases":
			if request.URL.Query().Get("per_page") != "30" {
				t.Fatalf("unexpected query: %s", request.URL.RawQuery)
			}
			fmt.Fprintf(response, `[
				{"tag_name":"v0.5.1-nightly.20261125.draft00","draft":true,"prerelease":true},
				{"tag_name":"v0.5.0","draft":false,"prerelease":false},
				{"tag_name":"v0.6.0-rc.1","draft":false,"prerelease":true},
				{"tag_name":"bad-nightly.1","draft":false,"prerelease":true},
				{"tag_name":"%s","draft":false,"prerelease":true},
				{"tag_name":"v0.5.1-nightly.20261123.def5678","draft":false,"prerelease":true}
			]`, nightlyTag)
		case "/downloads/" + nightlyTag + "/checksums.txt":
			fmt.Fprintf(response, "%x  machinist_0.5.1-nightly.20261124.abc1234_linux_amd64.tar.gz\n", digest)
		case "/downloads/" + nightlyTag + "/machinist_0.5.1-nightly.20261124.abc1234_linux_amd64.tar.gz":
			response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "machinist")
	if err := os.WriteFile(executable, []byte("old machinist"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Update(t.Context(), Options{
		Channel:     ChannelNightly,
		Current:     "v0.5.0",
		Executable:  executable,
		GOOS:        "linux",
		GOARCH:      "amd64",
		Client:      server.Client(),
		APIBase:     server.URL + "/api",
		ReleaseBase: server.URL + "/downloads",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != nightlyTag || result.AlreadyCurrent {
		t.Fatalf("result = %#v", result)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("installed binary = %q", got)
	}
}

func TestUpdateNightlyReportsAlreadyCurrent(t *testing.T) {
	const nightlyTag = "v0.5.1-nightly.20261124.abc1234"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/releases" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		fmt.Fprintf(response, `[{"tag_name":"%s","draft":false,"prerelease":true}]`, nightlyTag)
	}))
	defer server.Close()
	result, err := Update(t.Context(), Options{
		Channel: ChannelNightly,
		Current: nightlyTag,
		Client:  server.Client(),
		APIBase: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyCurrent || result.Version != nightlyTag {
		t.Fatalf("result = %#v", result)
	}
}

func TestUpdateNightlyErrorsWhenNonePublished(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fmt.Fprint(response, `[
			{"tag_name":"v0.5.0","draft":false,"prerelease":false},
			{"tag_name":"v0.5.1-nightly.20261124.draft00","draft":true,"prerelease":true},
			{"tag_name":"v0.6.0-rc.1","draft":false,"prerelease":true}
		]`)
	}))
	defer server.Close()
	_, err := Update(t.Context(), Options{
		Channel: ChannelNightly,
		Current: "v0.5.0",
		Client:  server.Client(),
		APIBase: server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "no nightly release found") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateRejectsInvalidChannelAndVersionChannelConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected network request: %s", request.URL.Path)
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "machinist")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Update(t.Context(), Options{
		Channel:    "beta",
		Executable: executable,
		Client:     server.Client(),
		APIBase:    server.URL,
	}); err == nil || !strings.Contains(err.Error(), "invalid release channel") {
		t.Fatalf("invalid channel error = %v", err)
	}

	if _, err := Update(t.Context(), Options{
		Version:    "v0.5.0",
		Channel:    ChannelNightly,
		Executable: executable,
		Client:     server.Client(),
		APIBase:    server.URL,
	}); err == nil || !strings.Contains(err.Error(), "cannot combine version") {
		t.Fatalf("version+channel conflict error = %v", err)
	}
}

func TestReplaceExecutablePreservesSymlinks(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%t", relative), func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "machinist-real")
			if err := os.WriteFile(target, []byte("old machinist"), 0o751); err != nil {
				t.Fatal(err)
			}
			linkTarget := target
			if relative {
				linkTarget = filepath.Base(target)
			}
			link := filepath.Join(directory, "machinist")
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Fatal(err)
			}
			if err := replaceExecutable(link, []byte("new machinist")); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{target, link} {
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != "new machinist" {
					t.Errorf("binary at %s = %q, want new machinist", path, got)
				}
			}
			if got, err := os.Readlink(link); err != nil || got != linkTarget {
				t.Errorf("symlink = %q, %v; want %q", got, err, linkTarget)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o751 {
				t.Errorf("permissions = %o, want 751", got)
			}
		})
	}
}

func testArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "machinist", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
