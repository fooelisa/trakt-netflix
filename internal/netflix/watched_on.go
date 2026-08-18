package netflix

import (
	"time"
)

// netflixDateLayouts are the formats seen in the viewing-activity table's
// date column. Netflix renders it per account locale, so we try the common
// ones rather than assuming one.
//
// Ex. "8/18/26" for an en-US account.
var netflixDateLayouts = []string{
	"1/2/06",
	"1/02/06",
	"01/02/06",
	"2/1/2006",
	"1/2/2006",
	"02/01/2006",
	"2006-01-02",
}

// ParseNetflixDate turns the raw date string from the viewing-activity
// table into a UTC timestamp.
//
// Netflix only gives a calendar day, no time, so the timestamp is midday
// UTC: it keeps the item on the correct day in every timezone Trakt might
// render it in, which midnight would not.
//
// The bool reports whether the date could be parsed. Callers should fall
// back to the current time only when it is false, and should say so - a
// silently wrong date is worse than a missing one, because it lands the
// item in today's history and looks like it was watched now.
func ParseNetflixDate(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range netflixDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

// WatchedAt returns the RFC3339 timestamp to report to Trakt for this
// activity, and whether it came from Netflix rather than being guessed.
func (h *WatchActivity) WatchedAt() (string, bool) {
	if t, ok := ParseNetflixDate(h.Date); ok {
		return t.Format(time.RFC3339), true
	}
	return time.Now().UTC().Format(time.RFC3339), false
}
