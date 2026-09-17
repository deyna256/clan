package codex_test

import (
	"context"
	"errors"
	"io"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
)

// consumeClientStream exercises the production Stream API and collects its
// terminal result for tests that need to inspect a complete response.
func consumeClientStream(
	client *codex.Client,
	ctx context.Context,
	a account.Account,
	request codex.Request,
) (codex.Result, error) {
	stream, err := client.Stream(ctx, a, request)
	if err != nil {
		return codex.Result{}, err
	}
	defer stream.Close()

	for {
		_, err = stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return stream.Result(), err
		}
	}
}
