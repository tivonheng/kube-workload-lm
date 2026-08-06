package schedule

import "fmt"

func ValidateWindow(window Window) error {
	if window.Name == "" {
		return fmt.Errorf("name must be non-empty")
	}
	if len(window.StartDays) == 0 {
		return fmt.Errorf("startDays must be non-empty")
	}
	seen := make(map[Day]struct{}, len(window.StartDays))
	for _, day := range window.StartDays {
		if !validDay(day) {
			return fmt.Errorf("invalid start day %q", day)
		}
		if _, exists := seen[day]; exists {
			return fmt.Errorf("duplicate start day %q", day)
		}
		seen[day] = struct{}{}
	}
	if window.AllDay {
		if window.Start != "" || window.End != "" {
			return fmt.Errorf("allDay cannot be combined with start or end")
		}
		return nil
	}
	start, err := parseMinute(window.Start)
	if err != nil {
		return fmt.Errorf("invalid start: %w", err)
	}
	end, err := parseMinute(window.End)
	if err != nil {
		return fmt.Errorf("invalid end: %w", err)
	}
	if start == end {
		return fmt.Errorf("start and end must differ")
	}
	return nil
}

func validDay(day Day) bool {
	switch day {
	case Monday, Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday:
		return true
	default:
		return false
	}
}
