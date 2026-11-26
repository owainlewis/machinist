package tickets

import (
	"context"
	"errors"
	"strings"
	"time"
)

// AcquireOrRenewLease atomically acquires an unheld or expired leader lease,
// or renews a lease already held by holderID. It returns true if holderID
// holds the lease after the call, or false if another holder holds an
// unexpired lease.
func (s *Store) AcquireOrRenewLease(ctx context.Context, name, holderID string, ttl time.Duration) (bool, error) {
	name = strings.TrimSpace(name)
	holderID = strings.TrimSpace(holderID)
	if name == "" || holderID == "" {
		return false, errors.New("lease name and holder ID are required")
	}
	if ttl <= 0 {
		return false, errors.New("lease TTL must be positive")
	}
	now := s.now().UTC()
	nowStamp := stamp(now)
	expStamp := stamp(now.Add(ttl))
	res, err := s.q().ExecContext(ctx, `
		INSERT INTO leader_leases(name, holder_id, lease_expires_at) VALUES(?, ?, ?)
		ON CONFLICT(name) DO UPDATE
		  SET holder_id = excluded.holder_id, lease_expires_at = excluded.lease_expires_at
		  WHERE leader_leases.holder_id = excluded.holder_id
		     OR leader_leases.lease_expires_at <= ?`,
		name, holderID, expStamp, nowStamp,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseLease deletes a leader lease if it is currently held by holderID.
func (s *Store) ReleaseLease(ctx context.Context, name, holderID string) error {
	name = strings.TrimSpace(name)
	holderID = strings.TrimSpace(holderID)
	if name == "" || holderID == "" {
		return nil
	}
	_, err := s.q().ExecContext(ctx, `DELETE FROM leader_leases WHERE name = ? AND holder_id = ?`, name, holderID)
	return err
}
