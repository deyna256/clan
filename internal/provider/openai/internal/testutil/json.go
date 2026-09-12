package testutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// EqualJSON compares JSON without rounding integers through float64.
func EqualJSON(t *testing.T, got, want string) {
	t.Helper()
	decode := func(raw string) any {
		t.Helper()
		require.True(t, json.Valid([]byte(raw)), "invalid JSON: %s", raw)
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		require.NoError(t, decoder.Decode(&value))
		return value
	}
	require.Equal(t, decode(want), decode(got))
}
