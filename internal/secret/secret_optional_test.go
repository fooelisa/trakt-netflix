package secret_test

import (
	"testing"

	"github.com/Nivl/trakt-netflix/internal/secret"
	"github.com/stretchr/testify/assert"
)

func TestGetOrEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "hunter2", secret.NewSecret("hunter2").GetOrEmpty())
	assert.Equal(t, "", secret.NewSecret("").GetOrEmpty())

	// The case that matters: a zero-value Secret is what an unset env var
	// leaves behind. Get() panics on it, which took down every token refresh
	// for a public OAuth client that has no secret to send.
	var unset secret.Secret
	assert.NotPanics(t, func() { _ = unset.GetOrEmpty() })
	assert.Equal(t, "", unset.GetOrEmpty())
	assert.Panics(t, func() { _ = unset.Get() }, "Get must still panic, callers rely on it")
}
