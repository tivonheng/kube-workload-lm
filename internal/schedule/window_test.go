package schedule

import (
	"testing"
	"time"
)

func TestMatchWindowShapesAndBoundaries(t *testing.T) {
	windows := []Window{
		{Name: "weekday", StartDays: []Day{Monday}, Start: "00:00", End: "08:00"},
		{Name: "overnight", StartDays: []Day{Friday}, Start: "20:00", End: "08:00"},
		{Name: "weekend", StartDays: []Day{Sunday}, AllDay: true},
	}
	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"start-inclusive", localTime(t, "Asia/Shanghai", 2026, 8, 3, 0, 0), "weekday"},
		{"end-exclusive", localTime(t, "Asia/Shanghai", 2026, 8, 3, 8, 0), ""},
		{"cross-day-start", localTime(t, "Asia/Shanghai", 2026, 8, 7, 20, 0), "overnight"},
		{"cross-day-end-day", localTime(t, "Asia/Shanghai", 2026, 8, 8, 7, 59), "overnight"},
		{"all-day", localTime(t, "Asia/Shanghai", 2026, 8, 9, 12, 0), "weekend"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, matched, err := Match(test.now, "Asia/Shanghai", windows)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want || matched != (test.want != "") {
				t.Fatalf("got (%q,%v), want %q", got, matched, test.want)
			}
		})
	}
}

func TestMatchDSTRepeatedHour(t *testing.T) {
	window := []Window{{Name: "repeated", StartDays: []Day{Sunday}, Start: "01:00", End: "02:00"}}
	for _, now := range []time.Time{time.Date(2024, 11, 3, 8, 30, 0, 0, time.UTC), time.Date(2024, 11, 3, 9, 30, 0, 0, time.UTC)} {
		if _, matched, err := Match(now, "America/Los_Angeles", window); err != nil || !matched {
			t.Fatalf("repeated local hour must match: matched=%v err=%v", matched, err)
		}
	}
}

func localTime(t *testing.T, zone string, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, location)
}

func TestMatchOverlapsUseStableNameAndTimezone(t *testing.T) {
	windows := []Window{
		{Name: "zulu", StartDays: []Day{Tuesday}, AllDay: true},
		{Name: "alpha", StartDays: []Day{Tuesday}, AllDay: true},
	}
	instant := time.Date(2026, 8, 3, 16, 30, 0, 0, time.UTC) // Tuesday 00:30 in Shanghai.
	name, matched, err := Match(instant, "Asia/Shanghai", windows)
	if err != nil || !matched || name != "alpha" {
		t.Fatalf("overlapping match = (%q,%v,%v), want stable alpha", name, matched, err)
	}
	if name, matched, err := Match(instant, "America/Los_Angeles", windows); err != nil || matched || name != "" {
		t.Fatalf("same instant in Monday timezone matched Tuesday: (%q,%v,%v)", name, matched, err)
	}
}

func TestMatchDSTSpringGapDoesNotCompensate(t *testing.T) {
	window := []Window{{Name: "missing-hour", StartDays: []Day{Sunday}, Start: "02:00", End: "03:00"}}
	// America/Los_Angeles jumps from 01:59 to 03:00 on this date, so no
	// physical instant is synthesized to compensate for the missing hour.
	for _, instant := range []time.Time{
		time.Date(2024, 3, 10, 9, 59, 0, 0, time.UTC),
		time.Date(2024, 3, 10, 10, 0, 0, 0, time.UTC),
	} {
		if name, matched, err := Match(instant, "America/Los_Angeles", window); err != nil || matched || name != "" {
			t.Fatalf("spring gap instant %s matched: (%q,%v,%v)", instant, name, matched, err)
		}
	}
}
