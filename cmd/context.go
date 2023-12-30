package cmd

import (
	"context"
	"os/signal"
	"syscall"
)

// ContextWithSignal create a context cancelled when SIGINT or SIGTERM are notified
func ContextWithSignal(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
}
