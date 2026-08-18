package netflix

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/Nivl/trakt-netflix/internal/errutil"
	"github.com/Nivl/trakt-netflix/internal/o11y"
	"github.com/PuerkitoBio/goquery"
)

// activityRow is a single row of the Netflix viewing-activity table.
type activityRow struct {
	title string
	date  string
}

// UpdateHistory Updates the viewing history from Netflix
func (c *Client) UpdateHistory(ctx context.Context, reporter o11y.Reporter) (err error) {
	slog.InfoContext(ctx, "Checking for new watched medias on Netflix")

	res, err := c.request(ctx, c.WatchActivityURL)
	if err != nil {
		return fmt.Errorf("make http request: %w", err)
	}

	defer errutil.RunAndSetError(res.Body.Close, &err, "close response body")
	defer errutil.RunAndSetError(func() error {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}, &err, "empty response body")

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return fmt.Errorf("parsing HTML: %w", err)
	}

	// Each row is:
	//   <li class="retableRow">
	//     <div class="col date">8/18/26</div>
	//     <div class="col title"><a href="/title/…">…</a></div>
	// The date column is the only record of WHEN something was watched.
	// Without it every item would be stamped with the sync time instead.
	newList := make([]activityRow, 0, HistorySize)
	for _, s := range doc.Find(".retableRow").EachIter() {
		newList = append(newList, activityRow{
			title: s.Find(".title").Find("a").Text(),
			date:  strings.TrimSpace(s.Find(".date").Text()),
		})
	}

	// we reverse the list to have the oldest entries first, and
	// newest last
	slices.Reverse(newList)
	for _, row := range newList {
		c.History.Push(ctx, cleanupString(row.title), row.date, reporter)
	}

	return nil
}

// cleanupString normalizes whitespace in a string.
//
// TODO(melvin): There's probably a cleaner way to do that.
func cleanupString(s string) string {
	out := strings.Builder{}
	lastIsSpace := true
	for _, r := range s {
		isSpace := unicode.IsSpace(r)
		if isSpace && lastIsSpace {
			continue
		}
		lastIsSpace = isSpace
		if isSpace {
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
