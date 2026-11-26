package tickets

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaderLeaseAcquireRenewStealAndRelease(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	ctx := t.Context()

	// Holder A acquires fresh lease.
	ok, err := store.AcquireOrRenewLease(ctx, "control-plane", "cp_a", 45*time.Second)
	if err != nil || !ok {
		t.Fatalf("holder A acquire = (%v, %v), want (true, nil)", ok, err)
	}

	// Holder B cannot steal unexpired lease (RowsAffected == 0).
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_b", 45*time.Second)
	if err != nil || ok {
		t.Fatalf("holder B steal before expiry = (%v, %v), want (false, nil)", ok, err)
	}

	// Holder A renews its own lease after 15s.
	now = now.Add(15 * time.Second)
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_a", 45*time.Second)
	if err != nil || !ok {
		t.Fatalf("holder A renew = (%v, %v), want (true, nil)", ok, err)
	}

	// Holder B releasing a lease it does not hold is a no-op.
	if err := store.ReleaseLease(ctx, "control-plane", "cp_b"); err != nil {
		t.Fatalf("holder B ReleaseLease: %v", err)
	}
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_b", 45*time.Second)
	if err != nil || ok {
		t.Fatalf("holder B acquire after non-holder release = (%v, %v), want (false, nil)", ok, err)
	}

	// After TTL expires, Holder B takes over.
	now = now.Add(45 * time.Second)
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_b", 45*time.Second)
	if err != nil || !ok {
		t.Fatalf("holder B acquire after TTL = (%v, %v), want (true, nil)", ok, err)
	}

	// Holder A can no longer renew after losing lease.
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_a", 45*time.Second)
	if err != nil || ok {
		t.Fatalf("holder A renew after steal = (%v, %v), want (false, nil)", ok, err)
	}

	// Holder B releases cleanly; Holder A acquires immediately without waiting for TTL.
	if err := store.ReleaseLease(ctx, "control-plane", "cp_b"); err != nil {
		t.Fatalf("holder B ReleaseLease: %v", err)
	}
	ok, err = store.AcquireOrRenewLease(ctx, "control-plane", "cp_a", 45*time.Second)
	if err != nil || !ok {
		t.Fatalf("holder A acquire after clean release = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestLeaderLeaseConcurrentContentionSingleWinner(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	var winners atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			holder := "cp_" + string(rune('a'+idx))
			ok, err := store.AcquireOrRenewLease(ctx, "control-plane", holder, 45*time.Second)
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("AcquireOrRenewLease error: %v", err)
	}
	if winners.Load() != 1 {
		t.Fatalf("winners = %d, want 1", winners.Load())
	}
}
