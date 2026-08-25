package trakt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNeedsRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// Two units in play: expiresIn is SECONDS (as Trakt returns it), while
	// time.Add takes a Duration in nanoseconds. Keeping them as separate
	// named values stops the two being mixed.
	const (
		weekSeconds  = 7 * 24 * 60 * 60
		weekDuration = 7 * 24 * time.Hour
	)

	testCases := []struct {
		name      string
		createdAt int64
		expiresIn int
		want      bool
	}{
		{
			name:      "fresh token is left alone",
			createdAt: now.Unix(),
			expiresIn: weekSeconds,
			want:      false,
		},
		{
			name:      "renewed once inside the 24h margin",
			createdAt: now.Add(-weekDuration + 23*time.Hour).Unix(),
			expiresIn: weekSeconds,
			want:      true,
		},
		{
			name:      "not renewed just outside the margin",
			createdAt: now.Add(-weekDuration + 25*time.Hour).Unix(),
			expiresIn: weekSeconds,
			want:      false,
		},
		{
			// The state that produced the TraktNetflixTokenNotRefreshing
			// alert: nothing to report for a week, so no request, no 401,
			// no refresh.
			name:      "already expired still triggers a refresh",
			createdAt: now.Add(-8 * 24 * time.Hour).Unix(),
			expiresIn: weekSeconds,
			want:      true,
		},
		{
			name:      "unauthenticated zero values never refresh",
			createdAt: 0,
			expiresIn: 0,
			want:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, needsRefresh(tc.createdAt, tc.expiresIn, now))
		})
	}
}
