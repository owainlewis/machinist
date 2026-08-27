package triggers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxCronSearchMinutes = 8 * 366 * 24 * 60

// Cron is a parsed five-field cron schedule in a specific IANA timezone.
// Next evaluates candidates in UTC so repeated local times remain distinct and
// nonexistent local times never appear.
type Cron struct {
	expression string
	location   *time.Location
	minute     cronField
	hour       cronField
	day        cronField
	month      cronField
	weekday    cronField
}

type cronField struct {
	min     int
	max     int
	values  []bool
	star    bool
	sunday7 bool
}

var monthNames = names(
	[]string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"},
	1,
)

var weekdayNames = names(
	[]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"},
	0,
)

func names(words []string, start int) map[string]int {
	result := make(map[string]int, len(words)*2)
	for index, word := range words {
		result[word] = start + index
		result[word[:3]] = start + index
	}
	return result
}

// ParseCron parses exactly five cron fields: minute, hour, day of month,
// month, and day of week. The timezone must be loadable from the IANA database.
func ParseCron(expression, timezone string) (*Cron, error) {
	if strings.ContainsAny(expression, "\r\n") {
		return nil, fmt.Errorf("cron schedule must be on one line")
	}
	if strings.Contains(expression, "=") || strings.Contains(strings.ToUpper(expression), "CRON_TZ") {
		return nil, fmt.Errorf("cron schedule must not contain an environment assignment")
	}
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron schedule must contain exactly five fields")
	}
	zone := strings.TrimSpace(timezone)
	if zone == "" {
		return nil, fmt.Errorf("cron timezone is required")
	}
	if zone != timezone || zone == "Local" {
		return nil, fmt.Errorf("cron timezone %q must be an IANA timezone name without surrounding whitespace", timezone)
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("load cron timezone %q: %w", zone, err)
	}

	minute, err := parseCronField(parts[0], "minute", 0, 59, nil, false)
	if err != nil {
		return nil, err
	}
	hour, err := parseCronField(parts[1], "hour", 0, 23, nil, false)
	if err != nil {
		return nil, err
	}
	day, err := parseCronField(parts[2], "day of month", 1, 31, nil, false)
	if err != nil {
		return nil, err
	}
	month, err := parseCronField(parts[3], "month", 1, 12, monthNames, false)
	if err != nil {
		return nil, err
	}
	weekday, err := parseCronField(parts[4], "day of week", 0, 7, weekdayNames, true)
	if err != nil {
		return nil, err
	}
	schedule := &Cron{
		expression: strings.Join(parts, " "),
		location:   location,
		minute:     minute,
		hour:       hour,
		day:        day,
		month:      month,
		weekday:    weekday,
	}
	// Eight years cover every weekday alignment and at least one leap year.
	// Reject schedules whose calendar constraints can never produce an instant
	// instead of allowing Next to return zero forever at runtime.
	if schedule.Next(time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)).IsZero() {
		return nil, fmt.Errorf("cron schedule has no possible occurrence")
	}
	return schedule, nil
}

func parseCronField(input, fieldName string, min, max int, named map[string]int, sunday7 bool) (cronField, error) {
	field := cronField{min: min, max: max, values: make([]bool, max-min+1), star: strings.HasPrefix(input, "*"), sunday7: sunday7}
	if input == "" {
		return cronField{}, fmt.Errorf("cron %s field is empty", fieldName)
	}
	for _, item := range strings.Split(input, ",") {
		if item == "" {
			return cronField{}, fmt.Errorf("cron %s field %q contains an empty list item", fieldName, input)
		}
		base, step, err := splitCronStep(item)
		if err != nil {
			return cronField{}, fmt.Errorf("cron %s field %q: %w", fieldName, input, err)
		}
		start, end, err := cronRange(base, min, max, named)
		if err != nil {
			return cronField{}, fmt.Errorf("cron %s field %q: %w", fieldName, input, err)
		}
		// In cron syntax, a step on one value means "from this value through
		// the field maximum" (for example, 5/15 in the minute field).
		if step > 1 && base != "*" && !strings.Contains(base, "-") {
			end = max
		}
		for value := start; value <= end; value += step {
			normalized := value
			if sunday7 && normalized == 7 {
				normalized = 0
			}
			field.values[normalized-min] = true
		}
	}
	return field, nil
}

func splitCronStep(input string) (string, int, error) {
	parts := strings.Split(input, "/")
	if len(parts) > 2 || len(parts) == 2 && (parts[0] == "" || parts[1] == "") {
		return "", 0, fmt.Errorf("invalid step")
	}
	if len(parts) == 1 {
		return input, 1, nil
	}
	step, err := strconv.Atoi(parts[1])
	if err != nil || step <= 0 {
		return "", 0, fmt.Errorf("step must be a positive number")
	}
	return parts[0], step, nil
}

func cronRange(input string, min, max int, named map[string]int) (int, int, error) {
	if input == "*" {
		return min, max, nil
	}
	parts := strings.Split(input, "-")
	if len(parts) > 2 || len(parts) == 2 && (parts[0] == "" || parts[1] == "") {
		return 0, 0, fmt.Errorf("invalid range")
	}
	start, err := cronValue(parts[0], min, max, named)
	if err != nil {
		return 0, 0, err
	}
	if len(parts) == 1 {
		return start, start, nil
	}
	end, err := cronValue(parts[1], min, max, named)
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, fmt.Errorf("range end must not precede its start")
	}
	return start, end, nil
}

func cronValue(input string, min, max int, named map[string]int) (int, error) {
	lower := strings.ToLower(input)
	if named != nil {
		if value, ok := named[lower]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(input)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("value %q must be between %d and %d", input, min, max)
	}
	return value, nil
}

func (c *Cron) Expression() string { return c.expression }

func (c *Cron) Timezone() string { return c.location.String() }

// Next returns the first scheduled UTC instant strictly after after.
func (c *Cron) Next(after time.Time) time.Time {
	candidate := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for range maxCronSearchMinutes {
		if c.matches(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

func (c *Cron) matches(candidate time.Time) bool {
	local := candidate.In(c.location)
	if !c.minute.has(local.Minute()) || !c.hour.has(local.Hour()) || !c.month.has(int(local.Month())) {
		return false
	}
	dayMatches := c.day.has(local.Day())
	weekdayMatches := c.weekday.has(int(local.Weekday()))
	if c.day.star || c.weekday.star {
		return dayMatches && weekdayMatches
	}
	return dayMatches || weekdayMatches
}

func (f cronField) has(value int) bool {
	if f.sunday7 && value == 7 {
		value = 0
	}
	return value >= f.min && value <= f.max && f.values[value-f.min]
}
