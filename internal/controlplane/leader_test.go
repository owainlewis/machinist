package controlplane

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
)

func TestLeaderSupervisorMutualExclusionAndCleanHandoff(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store := openTestStore(t)

	var activeCount atomic.Int32
	var maxActive atomic.Int32
	trackActive := func(started chan struct{}) func(context.Context) {
		return func(ctx context.Context) {
			cur := activeCount.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			activeCount.Add(-1)
		}
	}

	cfg := testConfig()
	s1, err := New(cfg, store, testFactory(), "v1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(cfg, store, testFactory(), "v1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	s1Started := make(chan struct{}, 1)
	s2Started := make(chan struct{}, 1)
	s1.bgWorkers = trackActive(s1Started)
	s2.bgWorkers = trackActive(s2Started)
	s1.leaderCfg = leaderConfig{
		holderID:        "cp_rev1",
		ttl:             10 * time.Second,
		renewInterval:   100 * time.Millisecond,
		acquireInterval: 50 * time.Millisecond,
		rpcTimeout:      2 * time.Second,
		safetyMargin:    500 * time.Millisecond,
		releaseTimeout:  2 * time.Second,
		drainTimeout:    2 * time.Second,
	}
	s2.leaderCfg = s1.leaderCfg
	s2.leaderCfg.holderID = "cp_rev2"

	ctx1, cancel1 := context.WithCancel(t.Context())
	done1 := make(chan struct{})
	go func() { defer close(done1); s1.runLeaderSupervisor(ctx1) }()

	// Wait for s1 to acquire leadership.
	select {
	case <-s1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("s1 did not acquire leadership")
	}

	// Start s2 while s1 holds the lease: s2 must stay standby.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); s2.runLeaderSupervisor(ctx2) }()

	select {
	case <-s2Started:
		t.Fatal("s2 acquired leadership while s1 still held lease")
	case <-time.After(250 * time.Millisecond):
	}
	if maxActive.Load() != 1 {
		t.Fatalf("maxActive = %d, want 1", maxActive.Load())
	}

	// Cancel s1 -> clean release -> s2 must take over in < 2s (well under 10s TTL!).
	handoffStart := time.Now()
	cancel1()
	<-done1

	select {
	case <-s2Started:
	case <-time.After(2 * time.Second):
		t.Fatal("s2 did not take over within 2s after clean release")
	}
	if elapsed := time.Since(handoffStart); elapsed >= 2*time.Second {
		t.Fatalf("handoff took %s, want < 2s", elapsed)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("maxActive during handoff = %d, want 1 (mutual exclusion violated)", maxActive.Load())
	}

	cancel2()
	<-done2
}

func TestLeaderSupervisorTTLFailoverAndStuckWorkerSkipsRelease(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store := openTestStore(t)

	// Simulate s1 stuck on shutdown: s1 must NOT release lease, so s2 waits until TTL expires.
	cfg := testConfig()
	s1, err := New(cfg, store, testFactory(), "v1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(cfg, store, testFactory(), "v1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	s1Started := make(chan struct{}, 1)
	unblockS1 := make(chan struct{})
	defer close(unblockS1)
	s1.bgWorkers = func(_ context.Context) {
		s1Started <- struct{}{}
		<-unblockS1 // intentionally ignores ctx cancellation to simulate stuck worker
	}

	s2Started := make(chan struct{}, 1)
	s2.bgWorkers = func(ctx context.Context) {
		s2Started <- struct{}{}
		<-ctx.Done()
	}

	s1.leaderCfg = leaderConfig{
		holderID:        "cp_stuck",
		ttl:             600 * time.Millisecond,
		renewInterval:   200 * time.Millisecond,
		acquireInterval: 50 * time.Millisecond,
		rpcTimeout:      2 * time.Second,
		safetyMargin:    100 * time.Millisecond,
		releaseTimeout:  2 * time.Second,
		drainTimeout:    100 * time.Millisecond,
	}
	s2.leaderCfg = s1.leaderCfg
	s2.leaderCfg.holderID = "cp_standby"

	ctx1, cancel1 := context.WithCancel(t.Context())
	done1 := make(chan struct{})
	go func() { defer close(done1); s1.runLeaderSupervisor(ctx1) }()

	select {
	case <-s1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("s1 did not acquire leadership")
	}

	// Shut down s1: drainTimeout expires before worker exits, so lease is NOT released.
	cancel1()
	<-done1

	// Immediately start s2: lease should still be held briefly, then fail over after TTL.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); s2.runLeaderSupervisor(ctx2) }()

	// s2 should NOT acquire immediately (< 150ms), since s1 skipped ReleaseLease.
	select {
	case <-s2Started:
		t.Fatal("s2 acquired immediately; stuck s1 should not have released lease")
	case <-time.After(150 * time.Millisecond):
	}

	// After the 600ms TTL expires, s2 must fail over cleanly.
	select {
	case <-s2Started:
	case <-time.After(2 * time.Second):
		t.Fatal("s2 did not fail over after TTL expiry")
	}

	cancel2()
	<-done2
}
