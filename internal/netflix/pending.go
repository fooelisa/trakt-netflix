package netflix

// Pending is an episode that has been seen in the Netflix viewing activity
// but deliberately not reported to Trakt yet.
//
// Netflix logs a title as soon as you start it and never records how far you
// got, so the newest episode of a show is ambiguous: it may have been watched
// in full, or abandoned after two minutes. Holding it until a LATER episode of
// the same season shows up resolves that - nobody watches episode 4 without
// finishing episode 3.
//
// Season finales are the exception and are released immediately, since nothing
// can ever supersede them (see activitytracker).
type Pending struct {
	// Title is the raw Netflix activity string, re-parsed on release so the
	// stored form stays independent of ParseTitle's internals.
	Title string `json:"title"`
	// Date is the raw date column from the viewing-activity table. Preserved
	// so a long-held item is still reported with the day it was watched,
	// not the day it was finally released.
	Date string `json:"date"`
}

// PendingKey identifies a held item.
func (p Pending) PendingKey() string {
	return p.Title
}

// HasPending reports whether an item is already being held.
func (h *History) HasPending(title string) bool {
	for i := range h.Pending {
		if h.Pending[i].Title == title {
			return true
		}
	}
	return false
}

// Hold adds an item to the pending set unless it is already there.
func (h *History) Hold(title, date string) {
	if h.HasPending(title) {
		return
	}
	h.Pending = append(h.Pending, Pending{Title: title, Date: date})
}

// ReleasePending removes the given titles from the pending set.
func (h *History) ReleasePending(released map[string]struct{}) {
	if len(released) == 0 {
		return
	}
	kept := make([]Pending, 0, len(h.Pending))
	for _, p := range h.Pending {
		if _, ok := released[p.Title]; !ok {
			kept = append(kept, p)
		}
	}
	h.Pending = kept
}
