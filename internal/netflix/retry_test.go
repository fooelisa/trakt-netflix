package netflix

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A match failure must not consume the item. Push used to mark it seen on
// sight, which made any failure permanent - Trakt's data for a currently
// airing season often lags Netflix by a day, so the first poll after watching
// is both the most likely to fail and the only one ever attempted.
func TestPushDoesNotMarkSeenUntilReported(t *testing.T) {
	t.Parallel()

	const raw = `Let's Marry Harry: The Reunion`

	h, err := NewHistory()
	require.NoError(t, err)

	h.Push(context.Background(), raw, "8/27/26", nil)
	require.Len(t, h.NewActivity, 1)
	assert.False(t, h.Has(raw), "an unreported item must not be marked seen")

	// A second run still offers it, because nothing accepted it yet.
	h.ClearNewActivity()
	h.Push(context.Background(), raw, "8/27/26", nil)
	assert.Len(t, h.NewActivity, 1, "unreported items must be retried")

	// Once Trakt accepts it, it is done.
	h.MarkSeen(raw)
	assert.True(t, h.Has(raw))
	h.ClearNewActivity()
	h.Push(context.Background(), raw, "8/27/26", nil)
	assert.Empty(t, h.NewActivity, "a reported item must not be offered again")
}

func TestMarkSeenIsIdempotentAndBounded(t *testing.T) {
	t.Parallel()

	h, err := NewHistory()
	require.NoError(t, err)

	h.MarkSeen("a")
	h.MarkSeen("a")
	assert.Len(t, h.Items, 1, "marking twice must not duplicate")

	overfill := HistorySize + 5
	for i := range overfill {
		h.MarkSeen(fmt.Sprintf("item-%d", i))
	}
	assert.LessOrEqual(t, len(h.Items), HistorySize, "the ledger must stay bounded")
	assert.Len(t, h.ItemsSearch, len(h.Items), "index and list must stay in step")
}
