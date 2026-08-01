package schedule

import (
	"fmt"
	"time"
)

// WindowInstanceID generates a unique identifier for the current window cycle.
// Format: "{windowName}:{YYYY-MM-DD}" where the date is the window's start date
// in the configured timezone for this cycle instance.
//
// For allDay windows: uses the current date in the configured timezone.
// For time-range windows:
//   - If start < end (same day): uses the current date.
//   - If start >= end (crosses midnight): if current time < end, uses previous day;
//     otherwise uses the current date.
//
// This ensures the same window instance (even crossing midnight) always produces the same ID.
func WindowInstanceID(now time.Time, timezone string, windowName string, windows []Window) (string, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("load timezone %q: %w", timezone, err)
	}

	var window *Window
	for i := range windows {
		if windows[i].Name == windowName {
			window = &windows[i]
			break
		}
	}
	if window == nil {
		return "", fmt.Errorf("window %q not found", windowName)
	}

	local := now.In(location)
	startDate := local

	if !window.AllDay {
		start, err := parseMinute(window.Start)
		if err != nil {
			return "", fmt.Errorf("window %q invalid start: %w", windowName, err)
		}
		end, err := parseMinute(window.End)
		if err != nil {
			return "", fmt.Errorf("window %q invalid end: %w", windowName, err)
		}

		// Cross-midnight window: start >= end
		if start >= end {
			minute := local.Hour()*60 + local.Minute()
			if minute < end {
				// We're in the early morning portion — window started yesterday.
				startDate = local.AddDate(0, 0, -1)
			}
		}
	}

	id := fmt.Sprintf("%s:%s", windowName, startDate.Format("2006-01-02"))
	return id, nil
}
