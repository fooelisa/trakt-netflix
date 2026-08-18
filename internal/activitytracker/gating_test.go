package activitytracker

import (
	"testing"

	"github.com/Nivl/trakt-netflix/internal/netflix"
	"github.com/Nivl/trakt-netflix/internal/trakt"
	"github.com/stretchr/testify/assert"
)

func season(number int, episodeNumbers ...int) trakt.Season {
	s := trakt.Season{Number: number}
	for _, n := range episodeNumbers {
		s.Episodes = append(s.Episodes, trakt.Episode{Season: number, Number: n})
	}
	return s
}

func TestEpisodeCount(t *testing.T) {
	t.Parallel()

	seasons := []trakt.Season{
		season(1, 1, 2, 3),
		season(3, 1, 2, 3, 4, 5, 6, 7, 8),
	}

	assert.Equal(t, 8, episodeCount(seasons, 3))
	assert.Equal(t, 3, episodeCount(seasons, 1))
	assert.Equal(t, 0, episodeCount(seasons, 2), "unknown season disables the finale rule")

	// Highest number rather than len(): a gappy season must not under-count,
	// or a mid-season episode would look like the finale.
	assert.Equal(t, 10, episodeCount([]trakt.Season{season(1, 1, 2, 10)}, 1))
}

func TestIsFinale(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		item resolved
		want bool
	}{
		{
			name: "last episode of the season",
			item: resolved{isShow: true, season: 3, number: 8, seasonEpisodeCount: 8},
			want: true,
		},
		{
			name: "mid season",
			item: resolved{isShow: true, season: 3, number: 7, seasonEpisodeCount: 8},
			want: false,
		},
		{
			name: "unknown episode count must not be treated as a finale",
			item: resolved{isShow: true, season: 3, number: 8, seasonEpisodeCount: 0},
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.item.isFinale())
		})
	}
}

func TestSeasonKeySeparatesShowsAndSeasons(t *testing.T) {
	t.Parallel()

	a := resolved{activity: &netflix.WatchActivity{Title: "Love Never Lies: Poland"}, season: 3}
	b := resolved{activity: &netflix.WatchActivity{Title: "Love Never Lies: Poland"}, season: 2}
	c := resolved{activity: &netflix.WatchActivity{Title: "Love Never Lies"}, season: 3}

	assert.NotEqual(t, seasonKey(&a), seasonKey(&b), "different seasons must not share a key")
	assert.NotEqual(t, seasonKey(&a), seasonKey(&c), "different shows must not share a key")
	assert.Equal(t, seasonKey(&a), seasonKey(&a))
}
