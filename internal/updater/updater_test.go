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
