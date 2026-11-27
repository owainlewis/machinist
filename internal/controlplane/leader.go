package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	defaultLeaderLeaseName       = "control-plane"
	defaultLeaseTTL              = 45 * time.Second
	defaultLeaseRenewInterval    = 15 * time.Second
	defaultLeaseAcquireInterval  = 2 * time.Second
	defaultLeaseRPCTimeout       = 5 * time.Second
	defaultLeaseSafetyMargin     = 5 * time.Second
	defaultLeaseReleaseTimeout   = 1500 * time.Millisecond
	defaultBackgroundDrainBudget = 8 * time.Second
)

type leaderConfig struct {
	leaseName       string
	holderID        string
	ttl             time.Duration
	renewInterval   time.Duration
	acquireInterval time.Duration
	rpcTimeout      time.Duration
	safetyMargin    time.Duration
	releaseTimeout  time.Duration
	drainTimeout    time.Duration
}

func (s *Server) effectiveLeaderConfig() leaderConfig {
	cfg := s.leaderCfg
	if cfg.leaseName == "" {
		cfg.leaseName = defaultLeaderLeaseName
	}
	if cfg.holderID == "" {
		cfg.holderID = newHolderID()
	}
	if cfg.ttl <= 0 {
		cfg.ttl = defaultLeaseTTL
	}
	if cfg.renewInterval <= 0 {
		cfg.renewInterval = defaultLeaseRenewInterval
	}
	if cfg.acquireInterval <= 0 {
		cfg.acquireInterval = defaultLeaseAcquireInterval
	}
	if cfg.rpcTimeout <= 0 {
		cfg.rpcTimeout = defaultLeaseRPCTimeout
	}
	if cfg.safetyMargin <= 0 {
		cfg.safetyMargin = min(defaultLeaseSafetyMargin, cfg.ttl/4)
	}
	if cfg.releaseTimeout <= 0 {
		cfg.releaseTimeout = defaultLeaseReleaseTimeout
	}
	if cfg.drainTimeout <= 0 {
		cfg.drainTimeout = defaultBackgroundDrainBudget
	}
	return cfg
}

func newHolderID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cp_%d", time.Now().UnixNano())
	}
	return "cp_" + hex.EncodeToString(buf)
}

// runBackgroundWorkers runs all periodic background worker groups until
// ctx is cancelled and waits for every worker goroutine to return.
func (s *Server) runBackgroundWorkers(ctx context.Context) {
	if s.bgWorkers != nil {
		s.bgWorkers(ctx)
		return
	}
	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		s.engine.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		s.schedule.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		s.runLinear(ctx)
	}()
	go func() {
		defer wg.Done()
		s.runGitHubIssues(ctx)
	}()
	go func() {
		defer wg.Done()
		s.runAuthPrune(ctx)
	}()
	wg.Wait()
}

// runLeaderSupervisor coordinates the four periodic background worker groups
// behind a database-backed leader lease so overlapping control-plane revisions
// never run duplicate background loops.
func (s *Server) runLeaderSupervisor(ctx context.Context) {
	s.runLeaderSupervisorWithDrain(ctx, nil)
}

func (s *Server) runLeaderSupervisorWithDrain(ctx context.Context, drainCtx context.Context) {
	if s.store == nil {
		s.runBackgroundWorkers(ctx)
		return
	}
	cfg := s.effectiveLeaderConfig()
	nowFn := s.now
	if nowFn == nil {
		nowFn = time.Now
	}

	var (
		leading         bool
		leaseValidUntil time.Time
		workerCancel    context.CancelFunc
		workerDone      chan struct{}
	)

	startWorkers := func() {
		wCtx, wCancel := context.WithCancel(ctx)
		wDone := make(chan struct{})
		go func() {
			defer close(wDone)
			s.runBackgroundWorkers(wCtx)
		}()
		leading = true
		workerCancel = wCancel
		workerDone = wDone
	}

	stopWorkers := func(timeout time.Duration, deadlineCtx context.Context) bool {
		if !leading {
			return true
		}
		workerCancel()
		if deadlineCtx != nil {
			select {
			case <-workerDone:
				leading = false
				return true
			case <-deadlineCtx.Done():
				return false
			}
		}
		if timeout <= 0 {
			<-workerDone
			leading = false
			return true
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-workerDone:
			leading = false
			return true
		case <-timer.C:
			return false
		}
	}

	for {
		if ctx.Err() != nil {
			break
		}
		attemptStart := nowFn().UTC()
		rpcTimeout := cfg.rpcTimeout
		if leading {
			stepDownAt := leaseValidUntil.Add(-cfg.safetyMargin)
			remaining := stepDownAt.Sub(attemptStart)
			if remaining <= 0 {
				if s.log != nil {
					fmt.Fprintf(s.log, "machinist: leader lease %q reached safety margin without renewal, stepping down\n", cfg.leaseName)
				}
				stopWorkers(0, nil)
				continue
			}
			if remaining < rpcTimeout {
				rpcTimeout = remaining
			}
		}
		rpcCtx, rpcCancel := context.WithTimeout(ctx, rpcTimeout)
		acquired, err := s.store.AcquireOrRenewLease(rpcCtx, cfg.leaseName, cfg.holderID, cfg.ttl)
		rpcCancel()

		if ctx.Err() != nil {
			break
		}

		var nextWait time.Duration
		switch {
		case err == nil && acquired:
			leaseValidUntil = attemptStart.Add(cfg.ttl)
			if !leading {
				if s.log != nil {
					fmt.Fprintf(s.log, "machinist: acquired leader lease %q (%s)\n", cfg.leaseName, cfg.holderID)
				}
				startWorkers()
			}
			nextWait = cfg.renewInterval
		case err == nil && !acquired:
			if leading {
				if s.log != nil {
					fmt.Fprintf(s.log, "machinist: lost leader lease %q (%s), stepping down\n", cfg.leaseName, cfg.holderID)
				}
				stopWorkers(0, nil)
			}
			nextWait = cfg.acquireInterval
		default:
			if s.log != nil {
				fmt.Fprintf(s.log, "machinist: leader lease %q check failed: %v\n", cfg.leaseName, err)
			}
			nextWait = cfg.acquireInterval
			if leading {
				stepDownAt := leaseValidUntil.Add(-cfg.safetyMargin)
				now := nowFn().UTC()
				if !now.Before(stepDownAt) {
					if s.log != nil {
						fmt.Fprintf(s.log, "machinist: leader lease %q nearing expiry after DB errors, stepping down\n", cfg.leaseName)
					}
					stopWorkers(0, nil)
				} else if remaining := stepDownAt.Sub(now); remaining < nextWait {
					nextWait = remaining
				}
			}
		}

		// Never sleep past the proactive step-down margin while leading.
		if leading {
			stepDownAt := leaseValidUntil.Add(-cfg.safetyMargin)
			if remaining := stepDownAt.Sub(nowFn().UTC()); remaining < nextWait {
				nextWait = max(remaining, time.Millisecond)
			}
		}

		timer := time.NewTimer(nextWait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}

	if leading {
		if stopWorkers(cfg.drainTimeout, drainCtx) {
			relCtx, relCancel := context.WithTimeout(context.Background(), cfg.releaseTimeout)
			if err := s.store.ReleaseLease(relCtx, cfg.leaseName, cfg.holderID); err != nil && s.log != nil {
				fmt.Fprintf(s.log, "machinist: release leader lease %q failed: %v\n", cfg.leaseName, err)
			}
			relCancel()
		} else if s.log != nil {
			fmt.Fprintf(s.log, "machinist: background workers did not exit before shutdown deadline; skipping lease release\n")
		}
	}
}
