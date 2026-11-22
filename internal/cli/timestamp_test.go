package cli

import (
	"bytes"
	"testing"
	"time"
)

func TestTimestampWriterStartsEveryLineWithTheTime(t *testing.T) {
	clock := time.Date(2026, 11, 22, 22, 13, 3, 535_000_000, time.FixedZone("BST", 3600))
	var out bytes.Buffer
	writer := newTimestampWriter(&out, func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	})
	for _, write := range []string{"machinist: one\nmachinist: two\n", "machinist: th", "ree\n"} {
		if n, err := writer.Write([]byte(write)); err != nil || n != len(write) {
			t.Fatalf("Write(%q) = %d, %v", write, n, err)
		}
	}
	want := "2026-11-22T21:13:04.535Z machinist: one\n" +
		"2026-11-22T21:13:05.535Z machinist: two\n" +
		"2026-11-22T21:13:06.535Z machinist: three\n"
	if out.String() != want {
		t.Fatalf("log =\n%s\nwant\n%s", out.String(), want)
	}
}
