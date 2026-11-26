package controlplane

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/githubapp"
)

// githubCacheTTL is how long GitHub lookups for /api/v1/setup and
// /api/v1/settings/github are reused before asking GitHub again.
const githubCacheTTL = 30 * time.Second

type cachedIdentity struct {
	value   githubapp.Identity
	expires time.Time
}

type cachedAccess struct {
	value   ghclient.Access
	expires time.Time
}

// githubLookupCache holds short-lived GitHub lookup results for the setup and
// GitHub settings endpoints. Only successful answers are cached so installing
// the GitHub App or recovering from a transient error shows up immediately.
type githubLookupCache struct {
	mu        sync.Mutex
	gen       uint64
	identity  *cachedIdentity
	installed map[string]time.Time
	access    map[string]cachedAccess
}

func repoCacheKey(repository string) string {
	return strings.ToLower(repository)
}

func (c *githubLookupCache) getIdentity(now time.Time) (githubapp.Identity, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.identity != nil && now.Before(c.identity.expires) {
		return c.identity.value, true, c.gen
	}
	return githubapp.Identity{}, false, c.gen
}

func (c *githubLookupCache) setIdentity(value githubapp.Identity, expires time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	c.identity = &cachedIdentity{value: value, expires: expires}
}

func (c *githubLookupCache) deleteIdentity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity = nil
}

func (c *githubLookupCache) getInstalled(repository string, now time.Time) (bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if expires, ok := c.installed[repoCacheKey(repository)]; ok && now.Before(expires) {
		return true, c.gen
	}
	return false, c.gen
}

func (c *githubLookupCache) setInstalled(repository string, expires time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	if c.installed == nil {
		c.installed = map[string]time.Time{}
	}
	c.installed[repoCacheKey(repository)] = expires
}

func (c *githubLookupCache) deleteInstalled(repository string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.installed, repoCacheKey(repository))
}

func (c *githubLookupCache) getAccess(repository string, now time.Time) (ghclient.Access, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.access[repoCacheKey(repository)]; ok && now.Before(entry.expires) {
		return entry.value, true, c.gen
	}
	return ghclient.Access{}, false, c.gen
}

func (c *githubLookupCache) setAccess(repository string, value ghclient.Access, expires time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	if c.access == nil {
		c.access = map[string]cachedAccess{}
	}
	c.access[repoCacheKey(repository)] = cachedAccess{value: value, expires: expires}
}

func (c *githubLookupCache) deleteAccess(repository string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.access, repoCacheKey(repository))
}

func (c *githubLookupCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *githubLookupCache) invalidateRepo(repository string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	key := repoCacheKey(repository)
	delete(c.installed, key)
	delete(c.access, key)
}

func (c *githubLookupCache) invalidateAccess(repository string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	delete(c.access, repoCacheKey(repository))
}

func (c *githubLookupCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.identity = nil
	clear(c.installed)
	clear(c.access)
}

func refreshRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh"))) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func (s *Server) currentTime() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *Server) appIdentity(ctx context.Context, app githubApp, refresh bool) (githubapp.Identity, error) {
	var gen uint64
	if !refresh {
		identity, ok, g := s.githubCache.getIdentity(s.currentTime())
		if ok {
			return identity, nil
		}
		gen = g
	} else {
		gen = s.githubCache.generation()
	}
	identity, err := app.Identity(ctx)
	if err != nil {
		s.githubCache.deleteIdentity()
		return githubapp.Identity{}, err
	}
	s.githubCache.setIdentity(identity, s.currentTime().Add(githubCacheTTL), gen)
	return identity, nil
}

func (s *Server) appInstalled(ctx context.Context, app githubApp, repository string, refresh bool) error {
	var gen uint64
	if !refresh {
		ok, g := s.githubCache.getInstalled(repository, s.currentTime())
		if ok {
			return nil
		}
		gen = g
	} else {
		gen = s.githubCache.generation()
	}
	if err := app.Installed(ctx, repository); err != nil {
		s.githubCache.deleteInstalled(repository)
		return err
	}
	s.githubCache.setInstalled(repository, s.currentTime().Add(githubCacheTTL), gen)
	return nil
}

func (s *Server) repoAccess(ctx context.Context, repository string, refresh bool) (ghclient.Access, error) {
	var gen uint64
	if !refresh {
		access, ok, g := s.githubCache.getAccess(repository, s.currentTime())
		if ok {
			return access, nil
		}
		gen = g
	} else {
		gen = s.githubCache.generation()
	}
	access, err := s.client.RepositoryAccess(ctx, repository)
	if err != nil {
		s.githubCache.deleteAccess(repository)
		return ghclient.Access{}, err
	}
	s.githubCache.setAccess(repository, access, s.currentTime().Add(githubCacheTTL), gen)
	return access, nil
}
