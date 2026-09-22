package app

import (
	"context"
	"log/slog"
	"time"
)

func (a *application) startRetention(retention time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	a.retentionCancel = cancel
	a.retentionWorkers.Go(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := a.pruneRequests(ctx, now.Add(-retention)); err != nil && ctx.Err() == nil {
					logger.WarnContext(ctx, "request retention failed")
				}
			}
		}
	})
}

func (a *application) pruneRequests(ctx context.Context, before time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := a.store.DeleteRequestsBefore(ctx, before)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
	}
}

func (a *application) stopRetention() {
	if a.retentionCancel != nil {
		a.retentionCancel()
	}
	a.retentionWorkers.Wait()
}
