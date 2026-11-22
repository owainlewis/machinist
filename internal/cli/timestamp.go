package cli

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// logTimeFormat is the time at the start of each control plane log line. It
// is RFC 3339 in UTC with milliseconds, so lines sort and compare as text.
const logTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// timestampWriter starts every line written through it with the time it began.
// The control plane logs from several goroutines, so writes are serialised.
type timestampWriter struct {
	mu      sync.Mutex
	out     io.Writer
	now     func() time.Time
	midLine bool
}

func newTimestampWriter(out io.Writer, now func() time.Time) *timestampWriter {
	return &timestampWriter{out: out, now: now}
}

func (w *timestampWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	buffer := make([]byte, 0, len(body)+32)
	for rest := body; len(rest) > 0; {
		if !w.midLine {
			buffer = w.now().UTC().AppendFormat(buffer, logTimeFormat)
			buffer = append(buffer, ' ')
		}
		line := rest
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			line = rest[:end+1]
		}
		buffer = append(buffer, line...)
		rest = rest[len(line):]
		w.midLine = line[len(line)-1] != '\n'
	}
	if _, err := w.out.Write(buffer); err != nil {
		return 0, err
	}
	return len(body), nil
}
