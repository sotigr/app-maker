package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a five-field cron schedule evaluated in UTC (spec 10.2):
// minute hour day-of-month month day-of-week.
type Cron struct {
	fields [5]map[int]bool
	src    string
}

var cronRanges = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

// ParseCron parses "*/5 * * * *" style expressions (lists, ranges, steps).
func ParseCron(s string) (*Cron, error) {
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields", s)
	}
	c := &Cron{src: s}
	for i, p := range parts {
		set := map[int]bool{}
		lo, hi := cronRanges[i][0], cronRanges[i][1]
		for _, item := range strings.Split(p, ",") {
			step := 1
			if base, st, ok := strings.Cut(item, "/"); ok {
				n, err := strconv.Atoi(st)
				if err != nil || n <= 0 {
					return nil, fmt.Errorf("cron %q: bad step %q", s, st)
				}
				step, item = n, base
			}
			from, to := lo, hi
			switch {
			case item == "*":
			case strings.Contains(item, "-"):
				a, b, _ := strings.Cut(item, "-")
				x, err1 := strconv.Atoi(a)
				y, err2 := strconv.Atoi(b)
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("cron %q: bad range %q", s, item)
				}
				from, to = x, y
			default:
				x, err := strconv.Atoi(item)
				if err != nil {
					return nil, fmt.Errorf("cron %q: bad value %q", s, item)
				}
				from, to = x, x
				if step > 1 {
					to = hi
				}
			}
			if i == 4 && to == 7 {
				to = 6
				set[0] = true // 7 is Sunday too
			}
			if from < lo || to > hi || from > to {
				return nil, fmt.Errorf("cron %q: %q out of range %d-%d", s, item, lo, hi)
			}
			for v := from; v <= to; v += step {
				set[v] = true
			}
		}
		c.fields[i] = set
	}
	return c, nil
}

// Next returns the first minute strictly after t that matches.
func (c *Cron) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !c.fields[3][int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !c.fields[2][t.Day()] || !c.fields[4][int(t.Weekday())] {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !c.fields[1][t.Hour()] {
			t = t.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if !c.fields[0][t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return limit
}

func (c *Cron) String() string { return c.src }
