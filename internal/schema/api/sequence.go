package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	rt "organon/internal/runtime/api"
)

// SequenceDef is a compiled x-sequence (ADR 0021): a string field that
// gets the next number of a series, gaplessly, when the feature is
// created or when action On runs.
type SequenceDef struct {
	Path string
	// Series names the counter; nil uses SeriesConst (default: the type
	// and field).
	Series      rt.Program
	SeriesConst string
	// Format renders the number, e.g. "INV-{year}-{n:06}".
	Format string
	// On is "" (create) or an action name.
	On string
}

var seqPlaceholder = regexp.MustCompile(`\{([a-z]+)(?::0?([0-9]{1,2}))?\}`)

// CheckSequenceFormat reports a format that is not literal text with
// {n} or {n:0W} once, and optionally {year}, {yy}, {month}, {day},
// {series}.
func CheckSequenceFormat(f string) error {
	ns := 0
	rest := seqPlaceholder.ReplaceAllStringFunc(f, func(m string) string {
		p := seqPlaceholder.FindStringSubmatch(m)
		switch p[1] {
		case "n":
			ns++
		case "year", "yy", "month", "day", "series":
			if p[2] != "" {
				ns += 100 // widths are for {n} only
			}
		default:
			ns += 100
		}
		return ""
	})
	if ns != 1 || strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("format is text with {n} (or {n:06}) once, and optionally {year}, {yy}, {month}, {day}, {series}")
	}
	return nil
}

// FormatSequence renders number n of series in format at time at (UTC).
func FormatSequence(format string, n int64, series string, at time.Time) string {
	at = at.UTC()
	return seqPlaceholder.ReplaceAllStringFunc(format, func(m string) string {
		p := seqPlaceholder.FindStringSubmatch(m)
		switch p[1] {
		case "n":
			s := strconv.FormatInt(n, 10)
			if w, _ := strconv.Atoi(p[2]); len(s) < w {
				s = strings.Repeat("0", w-len(s)) + s
			}
			return s
		case "year":
			return strconv.Itoa(at.Year())
		case "yy":
			return fmt.Sprintf("%02d", at.Year()%100)
		case "month":
			return fmt.Sprintf("%02d", int(at.Month()))
		case "day":
			return fmt.Sprintf("%02d", at.Day())
		case "series":
			return series
		}
		return m
	})
}
