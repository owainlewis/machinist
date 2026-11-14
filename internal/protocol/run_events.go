package protocol

import "encoding/json"

const MaxRunEventBatch = 128
const MaxRunEventBatchBytes = 512 << 10

type RunEvent struct {
	Sequence int64  `json:"sequence"`
	Time     string `json:"time"`
	Type     string `json:"type"`
	Stream   string `json:"stream,omitempty"`
	Message  string `json:"message,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Data     string `json:"data,omitempty"`
}
type RunEventBatch struct {
	Events []json.RawMessage `json:"events"`
}
type RunEventPage struct {
	Cursor int64             `json:"cursor"`
	Events []json.RawMessage `json:"events"`
}
