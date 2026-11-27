package runner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

const maxRecordedOutputBytes = 64 << 20

const MaxEventLogBytes = 32 << 20

const truncationEventReserve = 1 << 10

type Event struct {
	Sequence int64     `json:"sequence"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Stream   string    `json:"stream,omitempty"`
	Message  string    `json:"message,omitempty"`
	Encoding string    `json:"encoding,omitempty"`
	Data     string    `json:"data,omitempty"`
}

func (log *eventLog) appendOutput(stream string, data []byte) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.outputTruncated {
		return nil
	}
	remaining := log.outputLimit - log.outputBytes
	recorded := data
	truncated := int64(len(data)) > remaining
	if truncated {
		if remaining > 0 {
			recorded = data[:remaining]
		} else {
			recorded = nil
		}
	}
	if len(recorded) > 0 {
		event := Event{
			Type:     "process.output",
			Stream:   stream,
			Encoding: "base64",
			Data:     base64.StdEncoding.EncodeToString(recorded),
		}
		fits, err := log.eventFits(event, truncationEventReserve)
		if err != nil {
			return err
		}
		if !fits {
			truncated = true
		} else if err := log.writeEvent(event); err != nil {
			return fmt.Errorf("write event log: %w", err)
		} else {
			log.outputBytes += int64(len(recorded))
		}
	}
	if truncated {
		log.outputTruncated = true
		if err := log.writeEvent(Event{
			Type:    "process.output_truncated",
			Message: fmt.Sprintf("recording stopped after %d output bytes or %d event bytes; live output continues", log.outputLimit, log.fileLimit),
		}); err != nil {
			return fmt.Errorf("write event log: %w", err)
		}
	}
	return nil
}

type eventLog struct {
	mu              sync.Mutex
	file            *os.File
	sequence        int64
	outputBytes     int64
	outputLimit     int64
	fileBytes       int64
	fileLimit       int64
	outputTruncated bool
}

func newEventLog(path string) (*eventLog, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create event log: %w", err)
	}
	return &eventLog{file: file, outputLimit: maxRecordedOutputBytes, fileLimit: MaxEventLogBytes}, nil
}

func (log *eventLog) append(eventType, stream, message string) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.writeEvent(Event{
		Type:    eventType,
		Stream:  stream,
		Message: message,
	}); err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	return nil
}

func (log *eventLog) eventFits(event Event, reserve int64) (bool, error) {
	body, err := log.encodeEvent(event)
	if err != nil {
		return false, err
	}
	return log.fileBytes+int64(len(body))+reserve <= log.fileLimit, nil
}

func (log *eventLog) writeEvent(event Event) error {
	body, err := log.encodeEvent(event)
	if err != nil {
		return err
	}
	if log.fileBytes+int64(len(body)) > log.fileLimit {
		return fmt.Errorf("event log exceeds %d bytes", log.fileLimit)
	}
	written, err := log.file.Write(body)
	if err != nil {
		return err
	}
	if written != len(body) {
		return fmt.Errorf("write event log: wrote %d of %d bytes", written, len(body))
	}
	log.sequence++
	log.fileBytes += int64(written)
	return nil
}

func (log *eventLog) encodeEvent(event Event) ([]byte, error) {
	event.Sequence = log.sequence + 1
	event.Time = time.Now().UTC()
	body, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func (log *eventLog) sync() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.file.Sync(); err != nil {
		return fmt.Errorf("sync event log: %w", err)
	}
	return nil
}

func (log *eventLog) close() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.file.Sync(); err != nil {
		_ = log.file.Close()
		return fmt.Errorf("sync event log: %w", err)
	}
	if err := log.file.Close(); err != nil {
		return fmt.Errorf("close event log: %w", err)
	}
	return nil
}
