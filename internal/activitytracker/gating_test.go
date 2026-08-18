package activitytracker

import (
	"testing"

	"github.com/Nivl/trakt-netflix/internal/netflix"
	"github.com/Nivl/trakt-netflix/internal/trakt"
	"github.com/stretchr/testify/assert"
)

func season(number int, episodeNumbers ...int) trakt.Season {
	s := trakt.Season{ //nolint:exhaustruct // only the numbering matters here
		Number: number,
	}
	for _, n := range episodeNumbers {
		s.Episodes = append(s.Episodes, trakt.Episode{ //nolint:exhaustruct // only the numbering matters here
			Season: number,
			Number: n,
		})
	}
	return s
}

func showItem(title string, seasonNum, number, seasonEpisodeCount int) resolved {
	return resolved{
		activity:           &netflix.WatchActivity{Title: title, EpisodeName: "", IsShow: true, Season: seasonNum, Date: ""},
		isShow:             true,
		ids:                trakt.IDs{Trakt: 0, Slug: nil, IMDB: nil, TMDB: nil, TVDB: nil},
		season:             seasonNum,
		number:             number,
		seasonEpisodeCount: seasonEpisodeCount,
	}
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
			item: showItem("Love Never Lies", 3, 8, 8),
			want: true,
		},
		{
			name: "mid season",
			item: showItem("Love Never Lies", 3, 7, 8),
			want: false,
		},
		{
			name: "unknown episode count must not be treated as a finale",
			item: showItem("Love Never Lies", 3, 8, 0),
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

	polandS3 := showItem("Love Never Lies: Poland", 3, 1, 8)
	polandS2 := showItem("Love Never Lies: Poland", 2, 1, 8)
	otherS3 := showItem("Love Never Lies", 3, 1, 8)

	assert.NotEqual(t, seasonKey(&polandS3), seasonKey(&polandS2), "different seasons must not share a key")
	assert.NotEqual(t, seasonKey(&polandS3), seasonKey(&otherS3), "different shows must not share a key")

	same := showItem("Love Never Lies: Poland", 3, 4, 8)
	assert.Equal(t, seasonKey(&polandS3), seasonKey(&same), "same show and season must share a key regardless of episode")
}

// A held item must round-trip through the pending set. Hold and release key
// on the RAW Netflix string, not on WatchActivity.Title - ParseTitle rewrites
// Title to just the show name, so every episode of a show would collapse onto
// one key and holds would never be released.
func TestHoldKeyIsRawNotShowTitle(t *testing.T) {
	t.Parallel()

	const rawA = `Let's Marry Harry: Let's Marry Harry: "Kiss & Tell"`
	const rawB = `Let's Marry Harry: Let's Marry Harry: "The L Word"`

	a := netflix.ParseTitle(t.Context(), rawA, nil)
	b := netflix.ParseTitle(t.Context(), rawB, nil)

	assert.Equal(t, a.Title, b.Title, "same show, so Title collapses - that is why Raw exists")
	assert.NotEqual(t, a.Raw, b.Raw, "Raw must stay unique per episode")
	assert.Equal(t, rawA, a.Raw)
}
