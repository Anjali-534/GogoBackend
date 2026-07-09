// Package dateutil holds the shared IST-timezone-safe date-range resolution
// logic, originally built for the driver earnings/ledger feature
// (GetDriverLedger) and extracted here so every list endpoint that needs a
// "this week / this month / this year / all time" style filter calls the
// exact same tested boundary math instead of re-deriving it.
package dateutil

import (
	"fmt"
	"time"
)

// ISTLocation is a fixed +5:30 offset (never DST-adjusted, matching India)
// rather than time.LoadLocation("Asia/Kolkata") — the Alpine base image this
// backend ships on has no tzdata package installed, so LoadLocation would
// fail at runtime. A fixed zone needs no tzdata and is exactly correct for
// India regardless.
var ISTLocation = time.FixedZone("IST", 5*3600+30*60)

// Range is a resolved [Start, End] window plus a human-readable label.
type Range struct {
	Start time.Time
	End   time.Time
	Label string
}

// Resolve computes the [Start, End] window for a named range key, in IST.
// since is used only for "all_time" — that window starts from the record's
// own earliest relevant timestamp (e.g. a driver's joined_at, or the zero
// value to mean "no lower bound") rather than an arbitrary lookback.
// from/to are RFC3339 strings, used only when rangeKey == "custom".
// Unknown/empty keys fall back to "this_week", matching the earnings
// feature's existing default.
func Resolve(rangeKey string, since time.Time, from, to string) (string, Range) {
	now := time.Now().In(ISTLocation)
	startOfDay := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ISTLocation)
	}
	mondayOf := func(t time.Time) time.Time {
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7 // Sunday -> 7, so the offset below still lands on Monday
		}
		return startOfDay(t).AddDate(0, 0, -(wd - 1))
	}

	switch rangeKey {
	case "today":
		return rangeKey, Range{Start: startOfDay(now), End: now, Label: "Today"}
	case "last_week":
		thisMonday := mondayOf(now)
		lastMonday := thisMonday.AddDate(0, 0, -7)
		lastSunday := thisMonday.Add(-time.Second)
		return rangeKey, Range{
			Start: lastMonday, End: lastSunday,
			Label: fmt.Sprintf("%s - %s", lastMonday.Format("2 Jan 2006"), lastSunday.Format("2 Jan 2006")),
		}
	case "this_month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, ISTLocation)
		return rangeKey, Range{Start: first, End: now, Label: "This Month"}
	case "last_month":
		firstThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, ISTLocation)
		lastMonthEnd := firstThisMonth.Add(-time.Second)
		firstLastMonth := time.Date(lastMonthEnd.Year(), lastMonthEnd.Month(), 1, 0, 0, 0, 0, ISTLocation)
		return rangeKey, Range{
			Start: firstLastMonth, End: lastMonthEnd,
			Label: fmt.Sprintf("%s - %s", firstLastMonth.Format("2 Jan 2006"), lastMonthEnd.Format("2 Jan 2006")),
		}
	case "this_year":
		first := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, ISTLocation)
		return rangeKey, Range{Start: first, End: now, Label: "This Year"}
	case "last_year":
		firstThisYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, ISTLocation)
		lastYearEnd := firstThisYear.Add(-time.Second)
		firstLastYear := time.Date(lastYearEnd.Year(), 1, 1, 0, 0, 0, 0, ISTLocation)
		return rangeKey, Range{
			Start: firstLastYear, End: lastYearEnd,
			Label: fmt.Sprintf("%s - %s", firstLastYear.Format("2 Jan 2006"), lastYearEnd.Format("2 Jan 2006")),
		}
	case "all_time":
		start := since.In(ISTLocation)
		if start.IsZero() {
			start = time.Date(2000, 1, 1, 0, 0, 0, 0, ISTLocation)
		}
		return rangeKey, Range{Start: start, End: now, Label: "All Time"}
	case "custom":
		parsedFrom, errFrom := time.Parse(time.RFC3339, from)
		parsedTo, errTo := time.Parse(time.RFC3339, to)
		if errFrom != nil || errTo != nil {
			return "this_week", Range{Start: mondayOf(now), End: now, Label: "This Week"}
		}
		parsedFrom = parsedFrom.In(ISTLocation)
		parsedTo = parsedTo.In(ISTLocation)
		return rangeKey, Range{
			Start: parsedFrom, End: parsedTo,
			Label: fmt.Sprintf("%s - %s", parsedFrom.Format("2 Jan 2006"), parsedTo.Format("2 Jan 2006")),
		}
	case "this_week":
		return rangeKey, Range{Start: mondayOf(now), End: now, Label: "This Week"}
	default:
		return "this_week", Range{Start: mondayOf(now), End: now, Label: "This Week"}
	}
}

// ParseSort normalizes a `?sort=` query value to "ASC" or "DESC" for direct
// use in an ORDER BY clause. Defaults to DESC (newest first), matching every
// list view's existing default order.
func ParseSort(sort string) string {
	if sort == "asc" || sort == "oldest" {
		return "ASC"
	}
	return "DESC"
}
