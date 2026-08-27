package triggers

import (
	"strings"
	"testing"
	"time"
)

func TestCronSupportsNamesListsRangesAndSteps(t *testing.T) {
	schedule, err := ParseCron("5/10 9-17/2 * JAN,MARCH MON-FRI", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.January, 5, 8, 0, 0, 0, time.UTC) // Monday
	if got, want := schedule.Next(after), time.Date(2026, time.January, 5, 9, 5, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	if got, want := schedule.Next(time.Date(2026, time.January, 5, 9, 5, 0, 0, time.UTC)), time.Date(2026, time.January, 5, 9, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("stepped next = %s, want %s", got, want)
	}
}

func TestCronUsesVixieDayOfMonthWeekdayOR(t *testing.T) {
	schedule, err := ParseCron("0 0 13 * FRI", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schedule.Next(time.Date(2024, time.June, 12, 0, 0, 0, 0, time.UTC)), time.Date(2024, time.June, 13, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("day-of-month next = %s, want %s", got, want)
	}
	if got, want := schedule.Next(time.Date(2024, time.June, 13, 0, 0, 0, 0, time.UTC)), time.Date(2024, time.June, 14, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("weekday next = %s, want %s", got, want)
	}
}

func TestCronDSTGapDoesNotFireAndRepeatFiresTwice(t *testing.T) {
	schedule, err := ParseCron("30 1 * * *", "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	gapStart := time.Date(2025, time.March, 30, 0, 59, 0, 0, time.UTC)
	if got, want := schedule.Next(gapStart), time.Date(2025, time.March, 31, 0, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("after DST gap = %s, want %s", got, want)
	}

	fallStart := time.Date(2025, time.October, 26, 0, 0, 0, 0, time.UTC)
	first := schedule.Next(fallStart)
	second := schedule.Next(first)
	if want := time.Date(2025, time.October, 26, 0, 30, 0, 0, time.UTC); !first.Equal(want) {
		t.Fatalf("first repeated occurrence = %s, want %s", first, want)
	}
	if want := time.Date(2025, time.October, 26, 1, 30, 0, 0, time.UTC); !second.Equal(want) {
		t.Fatalf("second repeated occurrence = %s, want %s", second, want)
	}
}

func TestParseCronRejectsNonFiveFieldAndUnsafeForms(t *testing.T) {
	tests := []struct {
		expression string
		timezone   string
		want       string
	}{
		{"0 0 0 * * *", "UTC", "exactly five"},
		{"@daily", "UTC", "exactly five"},
		{"CRON_TZ=UTC 0 0 * * *", "UTC", "environment"},
		{"PATH=/bin 0 0 * * *", "UTC", "environment"},
		{"0 0 * * *\n", "UTC", "one line"},
		{"0 0 * * *", "Not/A_Zone", "timezone"},
		{"0 0 * * *", "", "required"},
		{"0 0 * * *", "Local", "IANA"},
		{"0 0 * * *", " UTC ", "IANA"},
		{"60 0 * * *", "UTC", "between 0 and 59"},
		{"*/0 0 * * *", "UTC", "positive"},
		{"0 0 * DEC-JAN *", "UTC", "must not precede"},
		{"0 0 31 2 *", "UTC", "no possible occurrence"},
	}
	for _, test := range tests {
		t.Run(test.expression+test.timezone, func(t *testing.T) {
			_, err := ParseCron(test.expression, test.timezone)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseCronAllowsImpossibleDayOfMonthWhenRestrictedWeekdayCanMatch(t *testing.T) {
	schedule, err := ParseCron("0 0 31 2 MON", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schedule.Next(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)), time.Date(2026, time.February, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}
