package netflix

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNetflixDate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		raw      string
		wantOK   bool
		wantDate string // YYYY-MM-DD
	}{
		{name: "en-US short year", raw: "8/18/26", wantOK: true, wantDate: "2026-08-18"},
		{name: "padded day", raw: "7/07/26", wantOK: true, wantDate: "2026-07-07"},
		{name: "ISO", raw: "2026-06-18", wantOK: true, wantDate: "2026-06-18"},
		{name: "empty is not a date", raw: "", wantOK: false},
		{name: "garbage is not a date", raw: "yesterday", wantOK: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ParseNetflixDate(tc.raw)
			require.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantDate, got.Format("2006-01-02"))
			// Midday UTC keeps the item on the right calendar day in every
			// timezone Trakt might render it in.
			assert.Equal(t, 12, got.Hour())
		})
	}
}

func TestWatchedAtFallsBackWhenDateUnusable(t *testing.T) {
	t.Parallel()

	good := &WatchActivity{Date: "8/18/26"}
	ts, fromNetflix := good.WatchedAt()
	require.True(t, fromNetflix)
	assert.Contains(t, ts, "2026-08-18")

	bad := &WatchActivity{Date: ""}
	_, fromNetflix = bad.WatchedAt()
	assert.False(t, fromNetflix, "an unparseable date must be reported, not silently stamped as now")
}

func TestPendingHoldAndRelease(t *testing.T) {
	t.Parallel()

	h := &History{}
	h.Hold("Show: Show: \"Ep 3\"", "8/18/26")
	h.Hold("Show: Show: \"Ep 3\"", "8/18/26") // idempotent
	require.Len(t, h.Pending, 1)
	assert.True(t, h.HasPending("Show: Show: \"Ep 3\""))

	h.Hold("Other: Other: \"Ep 1\"", "8/17/26")
	require.Len(t, h.Pending, 2)

	h.ReleasePending(map[string]struct{}{"Show: Show: \"Ep 3\"": {}})
	require.Len(t, h.Pending, 1)
	assert.False(t, h.HasPending("Show: Show: \"Ep 3\""))
	assert.True(t, h.HasPending("Other: Other: \"Ep 1\""))

	// releasing nothing must not wipe the set
	h.ReleasePending(nil)
	assert.Len(t, h.Pending, 1)
}
