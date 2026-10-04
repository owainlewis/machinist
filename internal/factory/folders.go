package factory

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type folderListing struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent"`
	Folders   []folderEntry `json:"folders"`
	Truncated bool          `json:"truncated"`
}

func (s *Service) folders(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	csrf := s.csrf
	host := r.URL.Query().Get("host")
	if host == "" {
		host = "local"
	}
	snapshot, known := s.cfg.Hosts[host]
	available := s.cfg.Enabled && !s.closed
	s.mu.Unlock()
	// Browsing belongs to the browser, never a conversation-scoped worker.
	if r.Header.Get("Authorization") != "" || (csrf != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Machinist-CSRF")), []byte(csrf)) != 1) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "browser authorization required", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || !strings.EqualFold(parsed.Host, r.Host) {
			http.Error(w, "invalid browser origin", 403)
			return
		}
	}
	if !available {
		fail(w, errors.New("factory is unavailable"))
		return
	}
	if !known {
		fail(w, errors.New("selected host is not configured"))
		return
	}
	selected := r.URL.Query().Get("path")
	if strings.ContainsAny(selected, "\x00\r\n") {
		fail(w, errors.New("folder path must be absolute and contain no control characters"))
		return
	}
	if selected != "" {
		clean, absolute := cleanProjectPath(host, selected)
		if !absolute {
			fail(w, errors.New("folder path must be absolute"))
			return
		}
		selected = clean
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var listing folderListing
	var err error
	if host == "local" {
		listing, err = localFolders(ctx, selected)
	} else {
		listing, err = remoteFolders(ctx, host, snapshot, selected)
	}
	if err != nil {
		fail(w, err)
		return
	}
	jsonReply(w, 200, listing)
}

func localFolders(ctx context.Context, selected string) (folderListing, error) {
	listing := folderListing{Folders: []folderEntry{}}
	var err error
	if selected == "" {
		selected, err = os.UserHomeDir()
		if err != nil {
			return listing, err
		}
	}
	selected, err = filepath.EvalSymlinks(selected)
	if err != nil {
		return listing, err
	}
	listing.Path = selected
	listing.Parent = filepath.Dir(selected)
	if listing.Parent == selected {
		listing.Parent = ""
	}
	directory, err := os.Open(selected)
	if err != nil {
		return listing, err
	}
	defer directory.Close()
	for {
		if err = ctx.Err(); err != nil {
			return listing, err
		}
		entries, readErr := directory.ReadDir(256)
		for _, entry := range entries {
			if !entry.IsDir() || strings.ContainsAny(entry.Name(), "\r\n") {
				continue
			}
			if len(listing.Folders) == 200 {
				listing.Truncated = true
				break
			}
			listing.Folders = append(listing.Folders, folderEntry{entry.Name(), filepath.Join(selected, entry.Name())})
		}
		if listing.Truncated || errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return listing, readErr
		}
	}
	sort.Slice(listing.Folders, func(i, j int) bool { return listing.Folders[i].Name < listing.Folders[j].Name })
	return listing, nil
}

// POSIX shell is already required by remote execution. NUL records preserve names
// without installing another runtime; paths are positional arguments, never code.
const remoteFolderScript = `selected=$1
if [ -z "$selected" ]; then selected=${HOME:-/}; fi
case "$selected" in /*) ;; *) exit 2;; esac
[ -d "$selected" ] || exit 3
cd "$selected" || exit 3
selected=$(pwd -P) || exit 3
printf '%s\000' "$selected"
count=0
for entry in ./* ./.[!.]* ./..?*; do
 [ -d "$entry" ] && [ ! -L "$entry" ] || continue
 name=${entry#./}
 if [ "$selected" = / ]; then target=/$name; else target=$selected/$name; fi
 printf '%s\000' "$target"
 count=$((count+1))
 [ "$count" -le 200 ] || break
done`

func remoteFolders(ctx context.Context, host string, snapshot config.FactoryHost, selected string) (folderListing, error) {
	listing := folderListing{Folders: []folderEntry{}}
	command := hostCommand(ctx, config.FactoryProject{Host: host}, snapshot, "", []string{"sh", "-c", remoteFolderScript, "machinist", selected})
	stdout, stderr := &boundedBuffer{limit: 64 << 10}, &boundedBuffer{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return listing, fmt.Errorf("could not browse selected host: %s (%w)", stderr.String(), err)
	}
	records := completeGitNames(string(stdout.data), stdout.truncated)
	if len(records) == 0 || !path.IsAbs(records[0]) {
		return listing, errors.New("selected host returned an invalid folder path")
	}
	listing.Path = records[0]
	listing.Parent = path.Dir(listing.Path)
	if listing.Parent == listing.Path {
		listing.Parent = ""
	}
	listing.Truncated = stdout.truncated || len(records)-1 > 200
	for _, entry := range records[1:] {
		if strings.ContainsAny(entry, "\r\n") || !path.IsAbs(entry) || path.Dir(entry) != listing.Path {
			continue
		}
		if len(listing.Folders) == 200 {
			listing.Truncated = true
			break
		}
		listing.Folders = append(listing.Folders, folderEntry{path.Base(entry), entry})
	}
	sort.Slice(listing.Folders, func(i, j int) bool { return listing.Folders[i].Name < listing.Folders[j].Name })
	return listing, nil
}
