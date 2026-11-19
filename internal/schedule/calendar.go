package schedule

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Named zones must work in the timer's minimal environment.
)

const (
	CalendarOnce     = "once"
	CalendarDaily    = "daily"
	CalendarWeekdays = "weekdays"
	CalendarWeekly   = "weekly"
	CalendarCron     = "cron"
)

// Calendar uses a named IANA time zone. It skips wall times inside a DST
// gap and uses the first instant when a wall time occurs twice.
type Calendar struct {
	Kind     string `json:"kind"`
	TimeZone string `json:"time_zone"`
	Time     string `json:"time,omitempty"`
	Date     string `json:"date,omitempty"`
	Weekday  string `json:"weekday,omitempty"`
	Cron     string `json:"cron,omitempty"`
}

type calendarRule struct {
	location *time.Location
	once     *time.Time
	fields   [5]uint64
	starDay  bool
	starWeek bool
}

const (
	allMinutes  = 1<<60 - 1
	allHours    = 1<<24 - 1
	allDays     = 1<<32 - 2
	allMonths   = 1<<13 - 2
	allWeekdays = 1<<7 - 1
)

func (c Calendar) compile() (calendarRule, error) {
	var rule calendarRule
	if c.TimeZone == "" || c.TimeZone == "Local" {
		return rule, fmt.Errorf("select a named IANA time zone")
	}
	var err error
	rule.location, err = time.LoadLocation(c.TimeZone)
	if err != nil {
		return rule, fmt.Errorf("time zone %q: %w", c.TimeZone, err)
	}
	rule.fields = [5]uint64{allMinutes, allHours, allDays, allMonths, allWeekdays}
	rule.starDay, rule.starWeek = true, true
	if c.Kind != CalendarCron {
		at, err := time.Parse("15:04", c.Time)
		if err != nil || len(c.Time) != 5 {
			return rule, fmt.Errorf("time must use HH:MM")
		}
		rule.fields[0], rule.fields[1] = 1<<at.Minute(), 1<<at.Hour()
	}
	switch c.Kind {
	case CalendarOnce:
		date, err := time.Parse("2006-01-02", c.Date)
		if err != nil {
			return rule, fmt.Errorf("date must use YYYY-MM-DD")
		}
		rule.once = &date
	case CalendarDaily:
	case CalendarWeekdays:
		rule.fields[4] = 0b0111110
	case CalendarWeekly:
		rule.fields[4] = 0
		for day := time.Sunday; day <= time.Saturday; day++ {
			if strings.EqualFold(day.String(), c.Weekday) || strings.EqualFold(day.String()[:3], c.Weekday) {
				rule.fields[4] = 1 << day
				break
			}
		}
		if rule.fields[4] == 0 {
			return rule, fmt.Errorf("select a weekday")
		}
	case CalendarCron:
		fields := strings.Fields(c.Cron)
		if len(fields) != 5 {
			return rule, fmt.Errorf("cron must contain five fields")
		}
		lower := [5]int{0, 0, 1, 1, 0}
		upper := [5]int{59, 23, 31, 12, 7}
		for i, field := range fields {
			rule.fields[i], err = cronField(field, lower[i], upper[i], i)
			if err != nil {
				return rule, fmt.Errorf("cron field %d: %w", i+1, err)
			}
		}
		rule.starDay = strings.HasPrefix(fields[2], "*")
		rule.starWeek = strings.HasPrefix(fields[4], "*")
		// Both 0 and 7 name Sunday.
		rule.fields[4] |= rule.fields[4] >> 7
		rule.fields[4] &= allWeekdays
	default:
		return rule, fmt.Errorf("unknown schedule kind %q", c.Kind)
	}
	return rule, nil
}

func cronField(field string, minValue, maxValue, index int) (uint64, error) {
	var mask uint64
	for part := range strings.SplitSeq(field, ",") {
		values, stepText, stepped := strings.Cut(part, "/")
		step := 1
		if stepped {
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step < 1 || step > maxValue-minValue+1 {
				return 0, fmt.Errorf("invalid step %q", stepText)
			}
		}
		start, end := minValue, maxValue
		if values != "*" {
			startText, endText, ranged := strings.Cut(values, "-")
			start = cronValue(startText, index)
			switch {
			case ranged:
				end = cronValue(endText, index)
			case !stepped:
				end = start
			}
		}
		if start < minValue || end > maxValue || start > end {
			return 0, fmt.Errorf("invalid range %q", values)
		}
		for v := start; v <= end; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

func cronValue(value string, index int) int {
	if number, err := strconv.Atoi(value); err == nil {
		return number
	}
	switch index {
	case 3:
		for month := time.January; month <= time.December; month++ {
			if strings.EqualFold(value, month.String()[:3]) {
				return int(month)
			}
		}
	case 4:
		for day := time.Sunday; day <= time.Saturday; day++ {
			if strings.EqualFold(value, day.String()[:3]) {
				return int(day)
			}
		}
	}
	return -1
}

// Next returns the first due instant strictly after after. A zero result
// means there is no upcoming occurrence within the supported calendar horizon.
func (c Calendar) Next(after time.Time) (time.Time, error) {
	rule, err := c.compile()
	if err != nil {
		return time.Time{}, err
	}
	return rule.find(after, false), nil
}

func (r calendarRule) find(bound time.Time, previous bool) time.Time {
	local := bound.In(r.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if r.once != nil {
		return r.onDay(*r.once, bound, previous)
	}
	// Eight leap years' worth of days also covers the skipped leap day at
	// a non-leap century (e.g. the gap between 2096 and 2104).
	step := 1
	if previous {
		step = -1
	}
	for range 8*366 + 1 {
		if r.matchesDay(day) {
			if due := r.onDay(day, bound, previous); !due.IsZero() {
				return due
			}
		}
		day = day.AddDate(0, 0, step)
	}
	return time.Time{}
}

func (r calendarRule) matchesDay(day time.Time) bool {
	if r.fields[3]&(1<<day.Month()) == 0 {
		return false
	}
	dom := r.fields[2]&(1<<day.Day()) != 0
	dow := r.fields[4]&(1<<day.Weekday()) != 0
	if r.starDay || r.starWeek {
		return dom && dow
	}
	return dom || dow
}

func (r calendarRule) onDay(day, bound time.Time, previous bool) time.Time {
	// Collect the offsets which can map onto this wall date, including
	// unusual shifts such as Lord Howe's 30-minute DST transition.
	offsets := map[int]struct{}{}
	end := day.Add(48 * time.Hour)
	for at := day.Add(-24 * time.Hour); at.Before(end); {
		local := at.In(r.location)
		_, offset := local.Zone()
		offsets[offset] = struct{}{}
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.After(at) {
			break
		}
		at = next
	}
	for hours := r.fields[1]; hours != 0; {
		hour := takeBit(&hours, previous)
		for minutes := r.fields[0]; minutes != 0; {
			minute := takeBit(&minutes, previous)
			wall := day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
			var first time.Time
			for offset := range offsets {
				at := wall.Add(-time.Duration(offset) * time.Second)
				local := at.In(r.location)
				if y, m, d := wall.Date(); local.Year() == y && local.Month() == m && local.Day() == d &&
					local.Hour() == hour && local.Minute() == minute && local.Second() == 0 &&
					(first.IsZero() || at.Before(first)) {
					first = at
				}
			}
			if !first.IsZero() && (previous != first.After(bound)) {
				return first.In(r.location)
			}
		}
	}
	return time.Time{}
}

func takeBit(mask *uint64, reverse bool) int {
	index := bits.TrailingZeros64(*mask)
	if reverse {
		index = bits.Len64(*mask) - 1
	}
	*mask &^= 1 << index
	return index
}
