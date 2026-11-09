package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// GitHub refuses a comment over 65536 characters.
	maxPostBytes = 60000
	// deliverBackoff keeps a GitHub outage to a few failed calls a minute.
	deliverBackoff = 30 * time.Second
)

// Poster posts a comment on an issue or PR.
type Poster func(ctx context.Context, repository string, number int, body string) error

func (e *Engine) runDeliveries(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.deliverDue(ctx)
		}
	}
}

func (e *Engine) deliverDue(ctx context.Context) {
	if e.now().Before(e.deliverNext) {
		return
	}
	if err := e.Deliver(ctx); err != nil && ctx.Err() == nil {
		e.deliverNext = e.now().Add(deliverBackoff)
		fmt.Fprintf(e.Log, "machinist: post messages: %v\n", err)
	}
}

// Deliver posts every waiting message. One failure does not hold up the rest. A
// crash between posting and recording can post a message twice.
func (e *Engine) Deliver(ctx context.Context) error {
	if e.Post == nil {
		return nil
	}
	messages, err := e.Store.UnpostedMessages(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, message := range messages {
		body := message.Body
		if len(body) > maxPostBytes {
			body = strings.ToValidUTF8(body[:maxPostBytes], "") + "\n[truncated]"
		}
		if err := e.Post(ctx, message.Repo, message.Number, body); err != nil {
			failures = append(failures, fmt.Errorf("%s#%d: %w", message.Repo, message.Number, err))
			continue
		}
		if err := e.Store.MarkPosted(ctx, message.EntryID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
