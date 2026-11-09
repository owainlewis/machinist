package agentrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func (d *Daemon) runDirectory(runID string) string {
	return filepath.Join(d.config.DataDirectory, "runs", runID)
}

func (d *Daemon) eventSpool(runID string) string {
	return filepath.Join(d.runDirectory(runID), "events.jsonl")
}

// flushRunEvents sends new lines of the run's event log. The file is the spool,
// and a torn last line is retried after the next write.
func (d *Daemon) flushRunEvents(ctx context.Context, runID string) error {
	path := d.eventSpool(runID)
	cursorPath := path + ".ack"
	type checkpoint struct {
		Cursor int64 `json:"cursor"`
		Offset int64 `json:"offset"`
	}
	var ack checkpoint
	if body, err := os.ReadFile(cursorPath); err == nil {
		if err = json.Unmarshal(body, &ack); err != nil {
			return fmt.Errorf("invalid event acknowledgment: %w", err)
		}
	}
	cursor, offset := ack.Cursor, ack.Offset
	if cursor < 0 || offset < 0 {
		return errors.New("invalid event acknowledgment")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if offset > info.Size() {
		return errors.New("event spool shorter than acknowledged offset")
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 128<<10)
	readOffset, safeOffset, batchEnd := offset, offset, offset
	save := func(nextCursor, nextOffset int64) error {
		body, _ := json.Marshal(checkpoint{Cursor: nextCursor, Offset: nextOffset})
		if err := os.WriteFile(cursorPath+".tmp", body, 0o600); err != nil {
			return err
		}
		if err := os.Rename(cursorPath+".tmp", cursorPath); err != nil {
			return err
		}
		cursor, offset = nextCursor, nextOffset
		return nil
	}
	batch := protocol.RunEventBatch{}
	bytes := 0
	send := func() error {
		if len(batch.Events) == 0 {
			if safeOffset > offset {
				return save(cursor, safeOffset)
			}
			return nil
		}
		var page protocol.RunEventPage
		if err := d.client.Post(ctx, "/api/v1/runs/"+runID+"/events", batch, &page); err != nil {
			return err
		}
		var last protocol.RunEvent
		_ = json.Unmarshal(batch.Events[len(batch.Events)-1], &last)
		if page.Cursor < last.Sequence {
			return errors.New("event acknowledgment behind sent batch")
		}
		if err := save(page.Cursor, batchEnd); err != nil {
			return err
		}
		safeOffset = batchEnd
		batch.Events = nil
		bytes = 0
		return nil
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
		readOffset += int64(len(line))
		var event protocol.RunEvent
		if json.Unmarshal(line, &event) != nil {
			return errors.New("corrupt event spool")
		}
		if event.Sequence <= cursor {
			safeOffset = readOffset
			continue
		}
		if len(batch.Events) >= protocol.MaxRunEventBatch || bytes+len(line) > protocol.MaxRunEventBatchBytes/2 {
			if err = send(); err != nil {
				return err
			}
		}
		batch.Events = append(batch.Events, json.RawMessage(line))
		bytes += len(line)
		batchEnd = readOffset
	}
	return send()
}

// streamRunEvents flushes activity while a run is live. The returned function
// stops it, so the final flush cannot run at the same time.
func (d *Daemon) streamRunEvents(ctx context.Context, runID string) func() {
	ctx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(d.eventInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				call, stop := context.WithTimeout(ctx, callTimeout)
				_ = d.flushRunEvents(call, runID)
				stop()
			}
		}
	}()
	return func() { cancel(); <-finished }
}
