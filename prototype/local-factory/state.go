package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type store struct {
	root string
	mu   sync.Mutex
}

func newStore(root string) *store { return &store{root: root} }

func (s *store) create(value issue) (work, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := workID(value.Repository, value.Number)
	if existing, err := s.readUnlocked(id); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return work{}, false, err
	}
	now := time.Now().UTC()
	item := work{
		ID:        id,
		Issue:     value,
		State:     stateQueued,
		Attempt:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Events:    []event{{At: now, Message: "work admitted"}},
	}
	if err := s.writeUnlocked(item); err != nil {
		return work{}, false, err
	}
	if err := os.WriteFile(filepath.Join(s.workDir(id), "issue.md"), []byte(renderIssue(value)), 0o644); err != nil {
		return work{}, false, fmt.Errorf("write issue snapshot: %w", err)
	}
	return item, true, nil
}

func (s *store) retry(id string) (work, bool, error) {
	item, err := s.get(id)
	if err != nil {
		return work{}, false, err
	}
	if item.State != stateFailed && item.State != stateBlocked {
		return item, false, nil
	}
	item, err = s.update(id, func(current *work) error {
		now := time.Now().UTC()
		current.State = stateQueued
		current.Attempt++
		current.ActiveRole = ""
		current.Failure = ""
		current.VerifyRuns = 0
		current.VerifiedSHA = ""
		current.StartedAt = time.Time{}
		current.CompletedAt = time.Time{}
		current.Events = append(current.Events, event{At: now, Message: fmt.Sprintf("attempt %d queued explicitly", current.Attempt)})
		return nil
	})
	return item, true, err
}

func (s *store) get(id string) (work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readUnlocked(id)
}

func (s *store) update(id string, fn func(*work) error) (work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.readUnlocked(id)
	if err != nil {
		return work{}, err
	}
	if err := fn(&item); err != nil {
		return work{}, err
	}
	item.UpdatedAt = time.Now().UTC()
	if err := s.writeUnlocked(item); err != nil {
		return work{}, err
	}
	return item, nil
}

func (s *store) event(id, message string) (work, error) {
	return s.update(id, func(item *work) error {
		item.Events = append(item.Events, event{At: time.Now().UTC(), Message: message})
		return nil
	})
}

func (s *store) list() ([]work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "work"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]work, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, err := s.readUnlocked(entry.Name())
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *store) artifact(id, name string, body []byte) error {
	if !validArtifact(name) {
		return errors.New("invalid artifact name")
	}
	if err := os.MkdirAll(s.workDir(id), 0o755); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.workDir(id), name), body, 0o644)
}

func (s *store) readArtifact(id, name string) ([]byte, error) {
	if !validArtifact(name) {
		return nil, errors.New("invalid artifact name")
	}
	return os.ReadFile(filepath.Join(s.workDir(id), name))
}

func (s *store) workDir(id string) string { return filepath.Join(s.root, "work", id) }

func (s *store) readUnlocked(id string) (work, error) {
	body, err := os.ReadFile(filepath.Join(s.workDir(id), "work.json"))
	if err != nil {
		return work{}, err
	}
	var item work
	if err := json.Unmarshal(body, &item); err != nil {
		return work{}, fmt.Errorf("decode work %q: %w", id, err)
	}
	return item, nil
}

func (s *store) writeUnlocked(item work) error {
	directory := s.workDir(item.ID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(filepath.Join(directory, "work.json"), body, 0o644)
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func workID(repository string, number int) string {
	return strings.NewReplacer("/", "--", ".", "-").Replace(strings.ToLower(repository)) + fmt.Sprintf("--%d", number)
}

func validArtifact(name string) bool {
	switch name {
	case "issue.md", "plan.md", "build.md", "review.md", "foreman.md":
		return true
	default:
		return false
	}
}

func renderIssue(value issue) string {
	return fmt.Sprintf("# %s\n\nRepository: %s\nIssue: #%d\nURL: %s\n\n%s\n", value.Title, value.Repository, value.Number, value.URL, value.Body)
}
