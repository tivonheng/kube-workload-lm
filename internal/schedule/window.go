package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Day string

const (
	Monday    Day = "MON"
	Tuesday   Day = "TUE"
	Wednesday Day = "WED"
	Thursday  Day = "THU"
	Friday    Day = "FRI"
	Saturday  Day = "SAT"
	Sunday    Day = "SUN"
)

type Window struct {
	Name      string `json:"name" yaml:"name"`
	StartDays []Day  `json:"startDays" yaml:"startDays"`
	Start     string `json:"start,omitempty" yaml:"start,omitempty"`
	End       string `json:"end,omitempty" yaml:"end,omitempty"`
	AllDay    bool   `json:"allDay,omitempty" yaml:"allDay,omitempty"`
}

func Match(now time.Time, timezone string, windows []Window) (string, bool, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", false, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	local := now.In(location)
	matches := make([]string, 0, len(windows))
	for _, window := range windows {
		matched, err := window.matches(local)
		if err != nil {
			return "", false, fmt.Errorf("window %q: %w", window.Name, err)
		}
		if matched {
			matches = append(matches, window.Name)
		}
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return "", false, nil
	}
	return matches[0], true, nil
}

func (window Window) matches(local time.Time) (bool, error) {
	if err := ValidateWindow(window); err != nil {
		return false, err
	}
	if window.AllDay {
		return containsDay(window.StartDays, fromWeekday(local.Weekday())), nil
	}
	start, err := parseMinute(window.Start)
	if err != nil {
		return false, fmt.Errorf("invalid start: %w", err)
	}
	end, err := parseMinute(window.End)
	if err != nil {
		return false, fmt.Errorf("invalid end: %w", err)
	}
	if start == end {
		return false, fmt.Errorf("start and end must differ")
	}
	minute := local.Hour()*60 + local.Minute()
	today := fromWeekday(local.Weekday())
	if start < end {
		return containsDay(window.StartDays, today) && minute >= start && minute < end, nil
	}
	previous := fromWeekday(local.AddDate(0, 0, -1).Weekday())
	return (containsDay(window.StartDays, today) && minute >= start) ||
		(containsDay(window.StartDays, previous) && minute < end), nil
}

func parseMinute(value string) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("time %q must use HH:MM", value)
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("time %q is out of range", value)
	}
	return hour*60 + minute, nil
}

func containsDay(days []Day, target Day) bool {
	for _, day := range days {
		if day == target {
			return true
		}
	}
	return false
}

func fromWeekday(day time.Weekday) Day {
	return [...]Day{Sunday, Monday, Tuesday, Wednesday, Thursday, Friday, Saturday}[day]
}
