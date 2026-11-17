package sources

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleIsTheFirstLineCutToFit(t *testing.T) {
	if got := title("  Fix the login  \n\nDetails"); got != "Fix the login" {
		t.Fatalf("title = %q", got)
	}
	long := title(strings.Repeat("é", 200))
	if utf8.RuneCountInString(long) != maxTitleRunes || !strings.HasSuffix(long, "…") {
		t.Fatalf("title = %q", long)
	}
}
