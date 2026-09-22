package artifacts

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFilesystemBoundedImmutablePublication(t *testing.T) {
	store, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Stage(strings.NewReader("12345"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	p, err := store.Stage(strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Size != 5 || p.Checksum != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatal(p)
	}
	if err = store.Publish(p, "task/run/file"); err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(p, "task/run/file"); err != nil {
		t.Fatal(err)
	}
	f, err := store.Open("task/run/file")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
	for _, path := range []string{"../secret", "/tmp/file", "a/../b", "a\\b", "."} {
		if ValidPath(path) {
			t.Fatalf("accepted %s", path)
		}
	}
}
