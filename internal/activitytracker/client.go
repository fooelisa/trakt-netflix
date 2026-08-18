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
}

// MarkAsWatched reports everything new in the Netflix viewing activity to
// Trakt.
//
// Netflix logs a title as soon as you start it and never records how far you
// got, so an abandoned episode is indistinguishable from a finished one and
// will be reported as watched. That is a deliberate trade-off for simplicity:
// the only completion signal Netflix exposes is its GraphQL Continue Watching
// row, which uses persisted query hashes tied to the frontend build and would
// break silently on any Netflix deploy.
func (c *Client) MarkAsWatched(ctx context.Context) {
	medias := new(trakt.MarkAsWatchedRequest)
	reported := 0

	for _, h := range c.netflixClient.History.NewActivity {
		r, err := c.searchMedia(ctx, h)
		if err != nil {
			c.slackClient.SendMessage(ctx, "Trakt: Couldn't find: "+h.String()+"\nError: "+err.Error()+"\nPlease add manually.")
			slog.ErrorContext(ctx, "media search failed", "isShow", h.IsShow, "media", h.String(), "error", err.Error())
			continue
		}

		watchedAt, fromNetflix := h.WatchedAt()
		if !fromNetflix {
			slog.WarnContext(ctx, "no usable Netflix date, falling back to now",
				"media", h.String(), "rawDate", h.Date)
		}
		m := trakt.MarkAsWatched{IDs: r.ids, WatchedAt: watchedAt}
		if r.isShow {
			medias.Episodes = append(medias.Episodes, m)
		} else {
			medias.Movies = append(medias.Movies, m)
		}
		reported++
		c.slackClient.SendMessage(ctx, "Adding to current watchlist batch: "+h.String())

		time.Sleep(100 * time.Millisecond)
	}

	slog.InfoContext(ctx, "batch built", "reporting", reported)
	if reported == 0 {
		c.netflixClient.History.ClearNewActivity()
		return
	}

	if _, err := c.traktClient.MarkAsWatched(ctx, medias); err != nil {
		c.slackClient.SendMessage(ctx, "Trakt: Couldn't mark the batch as watched. Error: "+err.Error())
		slog.ErrorContext(ctx, "failed to watch", "error", err.Error(), "medias", medias)
		return
	}

	c.slackClient.SendMessage(ctx, "Batch processed successfully")
	c.netflixClient.History.ClearNewActivity()
}

// searchMedia maps a Netflix movie/episode onto one on Trakt.
func (c *Client) searchMedia(ctx context.Context, h *netflix.WatchActivity) (*resolved, error) {
	if h.IsShow {
		episode, err := c.findEpisode(ctx, h)
		if err != nil {
			return nil, err
		}
		return &resolved{activity: h, isShow: true, ids: episode.IDs}, nil
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

// findEpisode maps a Netflix episode onto a Trakt one.
func (c *Client) findEpisode(ctx context.Context, h *netflix.WatchActivity) (*trakt.Episode, error) {
	showSearch, err := c.traktClient.Search(ctx, trakt.SearchRequest{
		Type:  trakt.SearchTypeShow,
		Query: h.SearchShow(),
		Show:  "",
	})
	if err != nil {
		return nil, fmt.Errorf("searching Trakt show (show=%q, episode=%q, activity=%s): %w", h.SearchShow(), h.EpisodeName, h.String(), err)
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
				return nil, fmt.Errorf("getting Trakt season episodes (show=%q, season=%d, activity=%s): %w", h.Title, h.Season, h.String(), err)
			}

			episode, err := findEpisodeInShowSeasons(h, []trakt.Season{{
				Number:   h.Season,
				IDs:      trakt.IDs{Trakt: 0, Slug: nil, IMDB: nil, TMDB: nil, TVDB: nil},
				Episodes: episodes,
			}})
			if err == nil {
				return episode, nil
			}
		}

		seasons, err := c.traktClient.GetShowSeasons(ctx, showID, true)
		if err != nil {
			return nil, fmt.Errorf("getting Trakt show seasons (show=%q, activity=%s): %w", h.Title, h.String(), err)
		}

		episode, err := findEpisodeInShowSeasons(h, seasons)
		if err == nil {
			return episode, nil
		}
		lastMatchErr = err
	}

	return nil, lastMatchErr
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
	// Curly vs straight quotes are the single most common mismatch after
	// accents, so normalize them before any comparison.
	netflixTitle = typographicQuotes.Replace(netflixTitle)
	traktTitle = typographicQuotes.Replace(traktTitle)

	if areEqual(netflixTitle, traktTitle) {
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
	if areEqual(netflixTitle, traktTitle) {
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

	return areEqual(netflixTitle, traktTitle)
}

func areEqual(a, b string) bool {
	if prefix, full, ok := truncatedPair(a, b); ok {
		return strings.HasPrefix(strings.ToLower(full), strings.ToLower(prefix))
	}
	return strings.EqualFold(a, b)
}

// truncatedPair spots Netflix's "..." truncation on EITHER side, returning
// the shortened title and the full one to compare it against.
//
// Every caller of stringMatches passes (traktTitle, netflixTitle) even though
// the parameters are named the other way round, so which side carries the
// "..." cannot be assumed. Checking only one side meant the truncation
// handling never fired in practice:
//
//	Netflix: "I Want To Know If I Can Trust You With..."
//	Trakt:   "I Want To Know If I Can Trust You With My Daughter"
//
// A title that is nothing but dots carries no signal, so it is rejected.
func truncatedPair(a, b string) (prefix, full string, ok bool) {
	aTruncated := strings.HasSuffix(a, "...")
	bTruncated := strings.HasSuffix(b, "...")

	switch {
	case aTruncated && !bTruncated:
		prefix = strings.TrimSuffix(a, "...")
		return prefix, b, prefix != ""
	case bTruncated && !aTruncated:
		prefix = strings.TrimSuffix(b, "...")
		return prefix, a, prefix != ""
	default:
		return "", "", false
	}
}
