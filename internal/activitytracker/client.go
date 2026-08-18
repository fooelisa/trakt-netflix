package activitytracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Nivl/trakt-netflix/internal/netflix"
	"github.com/Nivl/trakt-netflix/internal/slack"
	"github.com/Nivl/trakt-netflix/internal/trakt"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var wordStartingWithI = regexp.MustCompile(`(?m)(^|[\s\p{P}])i`)

var errMultipleEpisodeMatches = errors.New("multiple matching episodes found")

// Client represents a client to interact with external services
type Client struct {
	traktClient   *trakt.Client
	netflixClient *netflix.Client
	slackClient   *slack.Client
}

// New returns a new Client
func New(traktClient *trakt.Client, netflixClient *netflix.Client, slackClient *slack.Client) *Client {
	return &Client{
		slackClient:   slackClient,
		traktClient:   traktClient,
		netflixClient: netflixClient,
	}
}

// Run fetches the viewing history from Netflix and marks it as
// watched on Trakt
func (c *Client) Run(ctx context.Context) error {
	if err := c.UpdateHistory(ctx); err != nil {
		return err
	}
	c.MarkAsWatched(ctx)
	if err := c.netflixClient.History.Write(); err != nil {
		return fmt.Errorf("write history: %w", err)
	}
	return nil
}

// UpdateHistory fetches the viewing history from Netflix and
// updates the local history.
func (c *Client) UpdateHistory(ctx context.Context) error {
	err := c.netflixClient.UpdateHistory(ctx, c.slackClient)
	if err != nil {
		return fmt.Errorf("update history: %w", err)
	}
	return nil
}

// resolved is a Netflix activity that has been matched to a Trakt entity.
type resolved struct {
	activity *netflix.WatchActivity
	isShow   bool
	ids      trakt.IDs
	// season/number of the matched episode, 0 for movies
	season int
	number int
	// seasonEpisodeCount is how many episodes Trakt lists for that season.
	// 0 when unknown, which disables the finale rule for this item.
	seasonEpisodeCount int
}

// isFinale reports whether this is the last episode of its season. A finale
// can never be superseded, so holding it would mean never reporting it.
func (r *resolved) isFinale() bool {
	return r.seasonEpisodeCount > 0 && r.number == r.seasonEpisodeCount
}

// candidates returns everything eligible for reporting this run: items held
// back on a previous run, plus whatever is new in the Netflix activity.
func (c *Client) candidates(ctx context.Context) []*netflix.WatchActivity {
	h := c.netflixClient.History
	out := make([]*netflix.WatchActivity, 0, len(h.Pending)+len(h.NewActivity))
	for _, p := range h.Pending {
		a := netflix.ParseTitle(ctx, p.Title, nil)
		a.Date = p.Date
		out = append(out, a)
	}
	out = append(out, h.NewActivity...)
	return out
}

// MarkAsWatched reports finished media to Trakt.
//
// Netflix logs a title the moment you start it and never records progress, so
// the newest episode of a show is ambiguous. An episode is only reported once
// something proves it was finished:
//
//   - a LATER episode of the same season was also watched, or
//   - it is the season finale, which nothing can ever supersede.
//
// Anything else is held and re-evaluated on the next run. Movies are reported
// immediately: there is no equivalent signal for them.
func (c *Client) MarkAsWatched(ctx context.Context) {
	items := c.candidates(ctx)
	if len(items) == 0 {
		return
	}

	found := make([]resolved, 0, len(items))
	for _, h := range items {
		r, err := c.searchMedia(ctx, h)
		if err != nil {
			c.slackClient.SendMessage(ctx, "Trakt: Couldn't find: "+h.String()+"\nError: "+err.Error()+"\nPlease add manually.")
			slog.ErrorContext(ctx, "media search failed", "isShow", h.IsShow, "media", h.String(), "error", err.Error())
			continue
		}
		found = append(found, *r)
		time.Sleep(100 * time.Millisecond)
	}

	// Highest episode number seen per show+season across everything we
	// resolved this run. Only the current batch matters: an episode that
	// was already reported must itself have been superseded or a finale,
	// so it cannot be sitting above something still pending.
	highest := map[string]int{}
	for i := range found {
		r := &found[i]
		if !r.isShow {
			continue
		}
		k := seasonKey(r)
		if r.number > highest[k] {
			highest[k] = r.number
		}
	}

	medias := new(trakt.MarkAsWatchedRequest)
	released := map[string]struct{}{}
	held := 0

	for i := range found {
		r := &found[i]
		if r.isShow && !r.isFinale() && highest[seasonKey(r)] <= r.number {
			c.netflixClient.History.Hold(r.activity.Title, r.activity.Date)
			held++
			slog.InfoContext(ctx, "holding until a later episode confirms it was finished",
				"media", r.activity.String(), "season", r.season, "episode", r.number)
			continue
		}

		watchedAt, fromNetflix := r.activity.WatchedAt()
		if !fromNetflix {
			slog.WarnContext(ctx, "no usable Netflix date, falling back to now",
				"media", r.activity.String(), "rawDate", r.activity.Date)
		}
		m := trakt.MarkAsWatched{IDs: r.ids, WatchedAt: watchedAt}
		if r.isShow {
			medias.Episodes = append(medias.Episodes, m)
		} else {
			medias.Movies = append(medias.Movies, m)
		}
		released[r.activity.Title] = struct{}{}
		c.slackClient.SendMessage(ctx, "Adding to current watchlist batch: "+r.activity.String())
	}

	slog.InfoContext(ctx, "batch built", "reporting", len(released), "held", held)
	if len(released) == 0 {
		c.netflixClient.History.ClearNewActivity()
		return
	}

	if _, err := c.traktClient.MarkAsWatched(ctx, medias); err != nil {
		c.slackClient.SendMessage(ctx, "Trakt: Couldn't mark the batch as watched. Error: "+err.Error())
		slog.ErrorContext(ctx, "failed to watch", "error", err.Error(), "medias", medias)
		return
	}

	// Only drop holds once Trakt has actually accepted the batch.
	c.netflixClient.History.ReleasePending(released)
	c.slackClient.SendMessage(ctx, "Batch processed successfully")
	c.netflixClient.History.ClearNewActivity()
}

// seasonKey groups episodes of the same show and season together.
func seasonKey(r *resolved) string {
	return r.activity.Title + "\x00" + strconv.Itoa(r.season)
}

// searchMedia maps a Netflix movie/episode onto one on Trakt.
func (c *Client) searchMedia(ctx context.Context, h *netflix.WatchActivity) (*resolved, error) {
	if h.IsShow {
		episode, seasonCount, err := c.findEpisode(ctx, h)
		if err != nil {
			return nil, err
		}
		return &resolved{
			activity: h, isShow: true, ids: episode.IDs,
			season: episode.Season, number: episode.Number,
			seasonEpisodeCount: seasonCount,
		}, nil
	}

	response, err := c.traktClient.Search(ctx, trakt.SearchRequest{
		Type:  trakt.SearchTypeMovie,
		Query: h.SearchQuery(),
		Show:  h.SearchShow(),
	})
	if err != nil {
		return nil, fmt.Errorf("searching Trakt (query=%q, activity=%s): %w", h.SearchQuery(), h.String(), err)
	}

	for i := range response.Results {
		r := &response.Results[i]
		if r.Type == trakt.SearchTypeMovie && stringMatches(r.Movie.Title, h.Title) {
			return &resolved{activity: h, isShow: false, ids: r.Movie.IDs}, nil
		}
	}
	return nil, errors.New("not found")
}

// findEpisode maps a Netflix episode onto a Trakt one, and also reports how
// many episodes Trakt lists for the matched season. That count is what makes
// the finale rule possible - without it a season's last episode could never
// be confirmed and would be held forever.
func (c *Client) findEpisode(ctx context.Context, h *netflix.WatchActivity) (*trakt.Episode, int, error) {
	showSearch, err := c.traktClient.Search(ctx, trakt.SearchRequest{
		Type:  trakt.SearchTypeShow,
		Query: h.SearchShow(),
		Show:  "",
	})
	if err != nil {
		return nil, 0, fmt.Errorf("searching Trakt show (show=%q, episode=%q, activity=%s): %w", h.SearchShow(), h.EpisodeName, h.String(), err)
	}

	lastMatchErr := errors.New("not found")
	for i := range showSearch.Results {
		r := &showSearch.Results[i]
		if r.Type != trakt.SearchTypeShow || !stringMatches(r.Show.Title, h.Title) {
			continue
		}

		showID := showLookupID(r.Show)
		if h.Season > 0 {
			episodes, err := c.traktClient.GetSeasonEpisodes(ctx, showID, h.Season)
			if err != nil {
				return nil, 0, fmt.Errorf("getting Trakt season episodes (show=%q, season=%d, activity=%s): %w", h.Title, h.Season, h.String(), err)
			}

			seasons := []trakt.Season{{
				Number:   h.Season,
				IDs:      trakt.IDs{Trakt: 0, Slug: nil, IMDB: nil, TMDB: nil, TVDB: nil},
				Episodes: episodes,
			}}
			episode, err := findEpisodeInShowSeasons(h, seasons)
			if err == nil {
				return episode, episodeCount(seasons, episode.Season), nil
			}
		}

		seasons, err := c.traktClient.GetShowSeasons(ctx, showID, true)
		if err != nil {
			return nil, 0, fmt.Errorf("getting Trakt show seasons (show=%q, activity=%s): %w", h.Title, h.String(), err)
		}

		episode, err := findEpisodeInShowSeasons(h, seasons)
		if err == nil {
			return episode, episodeCount(seasons, episode.Season), nil
		}
		lastMatchErr = err
	}

	return nil, 0, lastMatchErr
}

// episodeCount returns the highest episode number Trakt lists for a season,
// or 0 when the season is unknown. Highest-number rather than len() because a
// partially populated season would otherwise under-count and make a
// mid-season episode look like the finale.
func episodeCount(seasons []trakt.Season, seasonNumber int) int {
	for i := range seasons {
		if seasons[i].Number != seasonNumber {
			continue
		}
		maxNum := 0
		for j := range seasons[i].Episodes {
			if n := seasons[i].Episodes[j].Number; n > maxNum {
				maxNum = n
			}
		}
		return maxNum
	}
	return 0
}

func findEpisodeInShowSeasons(h *netflix.WatchActivity, seasons []trakt.Season) (*trakt.Episode, error) {
	var seasonMatches []*trakt.Episode
	var allMatches []*trakt.Episode
	var specialMatches []*trakt.Episode

	for i := range seasons {
		season := &seasons[i]
		for j := range season.Episodes {
			episode := &season.Episodes[j]
			if !stringMatches(episode.Title, h.EpisodeName) {
				continue
			}

			allMatches = append(allMatches, episode)
			if season.Number == 0 {
				specialMatches = append(specialMatches, episode)
			}
			if h.Season > 0 && season.Number == h.Season {
				seasonMatches = append(seasonMatches, episode)
			}
		}
	}

	switch {
	case len(seasonMatches) == 1:
		return seasonMatches[0], nil
	case len(seasonMatches) > 1:
		return nil, errMultipleEpisodeMatches
	case h.Season > 0 && len(allMatches) == 1:
		return allMatches[0], nil
	case h.Season > 0 && len(allMatches) > 1:
		return nil, errMultipleEpisodeMatches
	case len(allMatches) == 1:
		return allMatches[0], nil
	case len(specialMatches) == 1:
		return specialMatches[0], nil
	case len(allMatches) > 1:
		return nil, errMultipleEpisodeMatches
	default:
		return nil, errors.New("not found")
	}
}

func showLookupID(show trakt.Media) string {
	if show.IDs.Slug != nil && *show.IDs.Slug != "" {
		return *show.IDs.Slug
	}
	return strconv.Itoa(show.IDs.Trakt)
}

// typographicQuotes maps the curly quote characters Trakt uses onto the
// straight ASCII ones Netflix uses.
//
// Ex.
//
//	Netflix title: "Let's Marry Harry"      (U+0027 apostrophe)
//	Trakt title:   "Let’s Marry Harry"      (U+2019 right single quote)
//
// Without this every episode of such a show fails to match and is reported
// as "not found".
var typographicQuotes = strings.NewReplacer(
	"\u2018", "'",
	"\u2019", "'",
	"\u201c", `"`,
	"\u201d", `"`,
	"\u2032", "'",
)

// Sometime the title don't match due to unicode characters.
// For example,
// On Netflix: "Arrested Development: Beef Consomme"
// On Trakt: "Arrested Development: Beef Consommé"
//
// So on top of regular title search, we also normalize the titles
// to remove accents and diacritics.
//
// Netflix and Trakt may also use different cases for the same title.
// For example,
// On Netflix: "Arrested Development: Justice is Blind"
// On Trakt: "Arrested Development: Justice Is Blind"
//
// There are a ton of other edge cases we need to account for.
func stringMatches(netflixTitle, traktTitle string) bool {
	// Netflix titles sometimes use "..." to indicate a longer title.
	titleIsPartial := strings.HasSuffix(netflixTitle, "...") && !strings.HasSuffix(traktTitle, "...")

	// Curly vs straight quotes are the single most common mismatch after
	// accents, so normalize them before any comparison.
	netflixTitle = typographicQuotes.Replace(netflixTitle)
	traktTitle = typographicQuotes.Replace(traktTitle)

	if areEqual(netflixTitle, traktTitle, titleIsPartial) {
		return true
	}

	stringNormalizer := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	netflixTitle, _, err := transform.String(stringNormalizer, netflixTitle)
	if err != nil {
		return false
	}
	traktTitle, _, err = transform.String(stringNormalizer, traktTitle)
	if err != nil {
		return false
	}
	if areEqual(netflixTitle, traktTitle, titleIsPartial) {
		return true
	}

	// Some characters aren't in the trakt title
	charsToReplace := []string{
		// Netflix title: "Arrested Development: Ready, Aim, Marry Me!"
		// Trakt title: "Arrested Development: Ready, Aim, Marry Me"
		"!",
	}

	// Special cases

	// if the title contains "!", then we need to take into account Spanish
	// Ex.
	//   Netflix title: "Arrested Development iAmigos!"
	//   Trakt title: "Arrested Development Amigos"
	//
	// In that example they used an "i" and not a "¡", which is a bit
	// awkward since it forces us to removes all "i"s at the beginning
	// of words.
	if strings.Contains(netflixTitle, "!") || strings.Contains(traktTitle, "!") {
		// We DO NOT remove the 'i's in B to avoid potentially breaking
		// valid titles
		// Ex:
		//   A: iiPhone!
		//   B: iPhone
		// If we cleanup both A and B we would end with
		//   A: iPhone
		//   B: Phone
		netflixTitle = wordStartingWithI.ReplaceAllStringFunc(netflixTitle, func(s string) string {
			// Keep the prefix (space or punctuation), drop the 'i'
			return s[:len(s)-1]
		})
		netflixTitle = strings.ReplaceAll(netflixTitle, "¡", "")
		traktTitle = strings.ReplaceAll(traktTitle, "¡", "")
	}

	for _, char := range charsToReplace {
		netflixTitle = strings.ReplaceAll(netflixTitle, char, "")
		traktTitle = strings.ReplaceAll(traktTitle, char, "")
	}

	// Another edge case we'd rather keep for the end
	// Sometime Netflix titles use spaces instead of dashes:
	// Ex.
	//   Netflix title: "Arrested Development: Forget Me Now"
	//   Trakt title: "Arrested Development: Forget-Me-Now"
	netflixTitle = strings.ReplaceAll(netflixTitle, " ", "-")
	traktTitle = strings.ReplaceAll(traktTitle, " ", "-")

	return areEqual(netflixTitle, traktTitle, titleIsPartial)
}

func areEqual(a, b string, titleIsPartial bool) bool {
	if titleIsPartial {
		// If the title is partial, we need to account for that in our comparison
		if len(a) < 3 {
			return false
		}
		return strings.HasPrefix(b, a[:len(a)-3])
	}
	return strings.EqualFold(a, b)
}
