package schedule

import (
	"testing"
	"time"
)

func TestWindowInstanceStart(t *testing.T) {
	windows := []Window{
		{Name: "weekend", StartDays: []Day{Saturday, Sunday}, AllDay: true},
		{Name: "weekday-morning", StartDays: []Day{Monday}, Start: "00:00", End: "08:00"},
		{Name: "overnight", StartDays: []Day{Friday}, Start: "20:00", End: "08:00"},
	}
	tests := []struct {
		name       string
		now        time.Time
		windowName string
		wantLocal  time.Time // expected start, expressed in Asia/Shanghai local wall time
	}{
		{
			name:       "all-day-start-is-local-midnight",
			now:        localTime(t, "Asia/Shanghai", 2026, 10, 10, 12, 0), // Saturday afternoon
			windowName: "weekend",
			wantLocal:  localTime(t, "Asia/Shanghai", 2026, 10, 10, 0, 0),
		},
		{
			name:       "same-day-range-start-is-start-minute",
			now:        localTime(t, "Asia/Shanghai", 2026, 8, 3, 3, 30), // Monday 03:30
			windowName: "weekday-morning",
			wantLocal:  localTime(t, "Asia/Shanghai", 2026, 8, 3, 0, 0),
		},
		{
			name:       "cross-midnight-evening-portion-uses-today",
			now:        localTime(t, "Asia/Shanghai", 2026, 8, 7, 22, 0), // Friday 22:00
			windowName: "overnight",
			wantLocal:  localTime(t, "Asia/Shanghai", 2026, 8, 7, 20, 0),
		},
		{
			name:       "cross-midnight-morning-portion-uses-previous-day",
			now:        localTime(t, "Asia/Shanghai", 2026, 8, 8, 7, 0), // Saturday 07:00 (still in Friday's window)
			windowName: "overnight",
			wantLocal:  localTime(t, "Asia/Shanghai", 2026, 8, 7, 20, 0),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := WindowInstanceStart(test.now, "Asia/Shanghai", test.windowName, windows)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(test.wantLocal) {
				t.Fatalf("got %s, want %s", got, test.wantLocal.UTC())
			}
			if got.Location() != time.UTC {
				t.Fatalf("result must be UTC, got location %s", got.Location())
			}
		})
	}
}

func TestWindowInstanceStartUnknownWindow(t *testing.T) {
	windows := []Window{{Name: "weekend", StartDays: []Day{Saturday}, AllDay: true}}
	if _, err := WindowInstanceStart(time.Now(), "Asia/Shanghai", "missing", windows); err == nil {
		t.Fatal("expected error for unknown window")
	}
}
