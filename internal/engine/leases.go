package engine

import (
	"context"
	"fmt"
	"time"
)

// LeaseDuration is how long a run survives without a heartbeat. It is stored in
// the database, so runs outlive a control plane restart.
const LeaseDuration = 10 * time.Minute

func (e *Engine) expireLeases(ctx context.Context) error {
	expired, err := e.Store.ExpireLeases(ctx)
	if err != nil {
		return err
	}
	if expired > 0 {
		fmt.Fprintf(e.Log, "machinist: %d runs lost their lease and were interrupted\n", expired)
	}
	return nil
}
