package agentrunner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func spoolDaemon(t *testing.T, url string) *Daemon {
	t.Helper()
	return &Daemon{config: config.Runner{DataDirectory: t.TempDir()}, client: NewClient(url, "token"), log: io.Discard, eventInterval: time.Millisecond}
}

func TestSpoolLostAckReplayAndPartialLine(t *testing.T) {
	events := map[int64]bool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls++
		var batch protocol.RunEventBatch
		if e := json.NewDecoder(r.Body).Decode(&batch); e != nil {
			t.Error(e)
		}
		var cursor int64
		for _, raw := range batch.Events {
			var event protocol.RunEvent
			_ = json.Unmarshal(raw, &event)
			events[event.Sequence] = true
			cursor = event.Sequence
		}
		if calls == 1 {
			http.Error(out, "ack lost", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(out).Encode(protocol.RunEventPage{Cursor: cursor})
	}))
	defer server.Close()
	daemon := spoolDaemon(t, server.URL)
	path := daemon.eventSpool("run")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("{\"sequence\":1,\"type\":\"agent.activity\"}\n{\"sequence\":2,\"type\":\"agent.message\"}"), 0o600)
	if err := daemon.flushRunEvents(context.Background(), "run"); err == nil {
		t.Fatal("lost ack accepted")
	}
	if err := daemon.flushRunEvents(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || calls != 2 {
		t.Fatalf("partial event or no replay: %v %d", events, calls)
	}
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = file.WriteString("\n")
	file.Close()
	if err := daemon.flushRunEvents(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("partial line not retried")
	}
	if err := daemon.flushRunEvents(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("acked events resent")
	}
}

func TestSpoolSeeksAcknowledgedPrefix(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls++
		var batch protocol.RunEventBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		var event protocol.RunEvent
		_ = json.Unmarshal(batch.Events[len(batch.Events)-1], &event)
		_ = json.NewEncoder(out).Encode(protocol.RunEventPage{Cursor: event.Sequence})
	}))
	defer server.Close()
	daemon := spoolDaemon(t, server.URL)
	path := daemon.eventSpool("run")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("{\"sequence\":1,\"type\":\"run.started\"}\n"), 0o600)
	if err := daemon.flushRunEvents(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	// An already acknowledged prefix is not read or parsed again.
	file, _ := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	_, _ = file.WriteString("{\"sequence\":2,\"type\":\"run.completed\"}\n")
	file.Close()
	file, _ = os.OpenFile(path, os.O_RDWR, 0o600)
	_, _ = file.WriteAt([]byte("BROKEN"), 0)
	file.Close()
	if err := daemon.flushRunEvents(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	var ack struct{ Cursor, Offset int64 }
	raw, _ := os.ReadFile(path + ".ack")
	_ = json.Unmarshal(raw, &ack)
	info, _ := os.Stat(path)
	if calls != 2 || ack.Cursor != 2 || ack.Offset != info.Size() {
		t.Fatalf("bad checkpoint: calls=%d ack=%+v", calls, ack)
	}
}

func TestFlushWithoutSpoolDoesNothing(t *testing.T) {
	daemon := spoolDaemon(t, "http://127.0.0.1:1")
	if err := daemon.flushRunEvents(t.Context(), "run_missing"); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRunEventsFinishesInFlightFlushOnStop(t *testing.T) {
	var mu sync.Mutex
	var received []int64
	calls := 0
	inFlight := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		var batch protocol.RunEventBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(out, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls++
		callNum := calls
		var cursor int64
		for _, raw := range batch.Events {
			var event protocol.RunEvent
			_ = json.Unmarshal(raw, &event)
			received = append(received, event.Sequence)
			cursor = event.Sequence
		}
		mu.Unlock()
		if callNum == 1 {
			close(inFlight)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(out).Encode(protocol.RunEventPage{Cursor: cursor})
	}))
	defer server.Close()

	daemon := spoolDaemon(t, server.URL)
	path := daemon.eventSpool("run")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"sequence\":1,\"type\":\"run.completed\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stopEvents := daemon.streamRunEvents(t.Context(), "run")
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for in-flight flush")
	}

	stopped := make(chan struct{})
	go func() {
		stopEvents()
		close(stopped)
	}()

	// Give stopEvents a moment to cancel if it would abort in-flight flushes,
	// then let the server answer the in-flight request.
	select {
	case <-stopped:
	case <-time.After(25 * time.Millisecond):
	}
	close(release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopEvents did not return")
	}
	// Stopping is idempotent.
	stopEvents()

	if err := daemon.flushRunEvents(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	gotCalls := calls
	gotReceived := append([]int64(nil), received...)
	mu.Unlock()
	if gotCalls != 1 || len(gotReceived) != 1 || gotReceived[0] != 1 {
		t.Fatalf("expected single flushed event without replay; calls=%d received=%v", gotCalls, gotReceived)
	}
}

func TestStreamRunEventsCancelsInFlightFlushWhenContextEnds(t *testing.T) {
	inFlight := make(chan struct{})
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-inFlight:
		default:
			close(inFlight)
		}
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	daemon := spoolDaemon(t, server.URL)
	path := daemon.eventSpool("run")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"sequence\":1,\"type\":\"run.started\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	stopEvents := daemon.streamRunEvents(ctx, "run")
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for in-flight flush")
	}

	cancel()
	stopped := make(chan struct{})
	go func() {
		stopEvents()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopEvents did not return after parent context cancellation")
	}
}
