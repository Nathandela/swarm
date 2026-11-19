package schedule

import (
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCalendarNext(t *testing.T) {
	tests := []struct {
		name        string
		calendar    Calendar
		after, want string
	}{
		{
			"skip spring gap",
			Calendar{Kind: CalendarDaily, TimeZone: "Europe/Zurich", Time: "02:30"},
			"2026-03-28T01:30:00Z", "2026-03-30T00:30:00Z",
		},
		{
			"first repeated time",
			Calendar{Kind: CalendarDaily, TimeZone: "Europe/Zurich", Time: "02:30"},
			"2026-10-24T00:30:00Z", "2026-10-25T00:30:00Z",
		},
		{
			"skip second repeated time",
			Calendar{Kind: CalendarDaily, TimeZone: "Europe/Zurich", Time: "02:30"},
			"2026-10-25T00:35:00Z", "2026-10-26T01:30:00Z",
		},
		{
			"Lord Howe half-hour gap",
			Calendar{Kind: CalendarDaily, TimeZone: "Australia/Lord_Howe", Time: "02:15"},
			"2026-10-02T15:45:00Z", "2026-10-04T15:15:00Z",
		},
		{
			"weekly day is local",
			Calendar{Kind: CalendarWeekly, TimeZone: "Asia/Tokyo", Time: "00:30", Weekday: "Monday"},
			"2026-11-21T15:30:00Z", "2026-11-22T15:30:00Z",
		},
		{
			"weekdays skip weekend",
			Calendar{Kind: CalendarWeekdays, TimeZone: "UTC", Time: "12:00"},
			"2026-11-20T12:00:00Z", "2026-11-23T12:00:00Z",
		},
		{
			"cron restricted day fields use OR",
			Calendar{Kind: CalendarCron, TimeZone: "UTC", Cron: "*/15 9-10 13 NOV MON"},
			"2026-11-12T12:00:00Z", "2026-11-13T09:00:00Z",
		},
		{
			"cron seven means Sunday",
			Calendar{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 9 * * 7"},
			"2026-11-20T12:00:00Z", "2026-11-22T09:00:00Z",
		},
		{
			"leap date across century",
			Calendar{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 9 29 Feb *"},
			"2096-02-29T09:00:00Z", "2104-02-29T09:00:00Z",
		},
		{
			"missing one-time date",
			Calendar{Kind: CalendarOnce, TimeZone: "Pacific/Apia", Time: "10:00", Date: "2011-12-30"},
			"2011-12-29T00:00:00Z", "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.calendar.Next(instant(t, test.after))
			if err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				if !got.IsZero() {
					t.Fatalf("got %v, want zero", got)
				}
			} else if !got.Equal(instant(t, test.want)) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestInvalidCalendar(t *testing.T) {
	check := []Calendar{
		{Kind: CalendarDaily, TimeZone: "Local", Time: "10:00"},
		{Kind: CalendarDaily, TimeZone: "Not/Somewhere", Time: "10:00"},
		{Kind: CalendarDaily, TimeZone: "UTC", Time: "25:12"},
		{Kind: CalendarWeekly, TimeZone: "UTC", Time: "10:00"},
		{Kind: CalendarOnce, TimeZone: "UTC", Time: "10:00", Date: "2026-02-29"},
		{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 0 12 * * *"},
		{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 0 * * */0"},
		{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 0 * * 5-2"},
		{Kind: CalendarCron, TimeZone: "UTC", Cron: "0 0 * * 8"},
	}
	for _, c := range check {
		if _, err := c.Next(time.Now()); err == nil {
			t.Errorf("%+v should fail", c)
		}
	}
}
