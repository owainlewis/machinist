package managedworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/artifacts"
	"github.com/owainlewis/machinist/internal/protocol"
)

func (c *Client) artifactRequest(ctx context.Context, method, path string, body io.Reader, spec protocol.RunSpec, instance string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Machinist-Instance", instance)
	req.Header.Set("X-Machinist-Lease", spec.LeaseToken)
	client := *c.http
	client.Timeout = 5 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &ResponseError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return resp, nil
}
func (w *Worker) prepareInputs(spec protocol.RunSpec) func(context.Context, string) (map[string]string, error) {
	return func(ctx context.Context, dir string) (map[string]string, error) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		paths := map[string]string{}
		for alias, a := range spec.Inputs {
			shared := strings.HasPrefix(alias, "__workspace__/")
			name := strings.TrimPrefix(alias, "__workspace__/")
			if !artifacts.ValidPath(name) || (!shared && strings.Contains(alias, "/")) {
				return nil, errors.New("invalid input alias")
			}
			dest := filepath.Join(dir, alias)
			if shared {
				dest = filepath.Join(filepath.Dir(dir), "outputs", filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					return nil, err
				}
			}
			err := w.retryArtifact(ctx, func() error {
				resp, err := w.client.artifactRequest(ctx, "GET", "/api/v1/artifacts/"+url.PathEscape(a.ID)+"/content", nil, spec, w.instanceID)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if err != nil {
					return &ResponseError{Status: 422, Body: err.Error()}
				}
				hash := sha256.New()
				n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, a.Size+1))
				closeErr := f.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return &ResponseError{Status: 422, Body: closeErr.Error()}
				}
				if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.Checksum {
					return &ResponseError{Status: 422, Body: "input checksum mismatch"}
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("materialize input %s: %w", alias, err)
			}
			paths[alias] = dest
		}
		return paths, nil
	}
}
func (w *Worker) retryArtifact(ctx context.Context, fn func() error) error {
	backoff := 250 * time.Millisecond
	for {
		err := fn()
		if err == nil {
			return nil
		}
		var e *ResponseError
		if errors.As(err, &e) && !e.Retryable() {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(w.stderr, "machinist: artifact transfer: %v; retrying\n", err)
		if !wait(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}
func (w *Worker) publishOutputs(ctx context.Context, spec protocol.RunSpec, dir string) ([]string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("output directory must be a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Freeze each file into a private spool before sending it. Retries send identical bytes.
	spool, err := os.MkdirTemp(filepath.Dir(dir), "publish-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(spool)
	var ids []string
	var total int64
	count := 0
	limits := spec.ArtifactLimits
	if limits == nil {
		return nil, errors.New("missing artifact limits")
	}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".pyc") || strings.HasSuffix(entry.Name(), ".pyo") {
			return nil
		}
		if !artifacts.ValidPath(name) || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("output %q must be a regular file", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output %q must be a regular file", name)
		}
		count++
		if count > 1000 || info.Size() > limits.MaxFileBytes || info.Size() > limits.MaxRunBytes-total {
			return errors.New("output size or file count limit exceeded")
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		opened, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return statErr
		}
		if !opened.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("output %q must be a regular file", name)
		}
		copy, err := os.CreateTemp(spool, "file-")
		if err != nil {
			f.Close()
			return err
		}
		n, err := io.Copy(copy, io.LimitReader(f, limits.MaxFileBytes+1))
		f.Close()
		closeErr := copy.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if n > limits.MaxFileBytes || n > limits.MaxRunBytes-total {
			return errors.New("output size limit exceeded")
		}
		total += n
		var a protocol.Artifact
		err = w.retryArtifact(ctx, func() error {
			file, err := os.Open(copy.Name())
			if err != nil {
				return &ResponseError{Status: 422, Body: err.Error()}
			}
			defer file.Close()
			resp, err := w.client.artifactRequest(ctx, "PUT", "/api/v1/runs/"+url.PathEscape(spec.ID)+"/artifacts?path="+url.QueryEscape(name), file, spec, w.instanceID)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			return json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&a)
		})
		if err != nil {
			return err
		}
		ids = append(ids, a.ID)
		return os.Remove(copy.Name())
	})
	return ids, err
}
