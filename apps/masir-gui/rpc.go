package main

import (
	"context"
	"strings"
	"time"

	"github.com/Gozargah/Masir/libs/masir-core/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func isConnectionDrop(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Unavailable && strings.Contains(st.Message(), "connection error")
}

func humanizeRPCError(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return tr(err.Error())
	}

	switch {
	case isConnectionDrop(err):
		return tr("can't reach the masir daemon")
	case st.Code() == codes.Unimplemented:
		return tr("the running masir daemon is older than this app; update masir")
	case client.ErrorReason(err) == "ROOT_REQUIRED":
		return tr("TUN needs the daemon to run as root")
	default:
		return tr(st.Message())
	}
}

const (
	rpcAttempts  = 3
	rpcRetryWait = 50 * time.Millisecond
)

func callRPC[T any](call func(context.Context) (T, error)) (T, error) {
	return retryRPC(context.Background(), func() (T, error) {
		ctx, cancel := rpc()
		defer cancel()
		return call(ctx)
	})
}

// callRPCContext shares the caller's deadline across all attempts.
func callRPCContext[T any](ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	return retryRPC(ctx, func() (T, error) { return call(ctx) })
}

func retryRPC[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T

	for attempt := 0; attempt < rpcAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := call()

		if err == nil {
			return value, nil
		}
		if status.Code(err) != codes.Unavailable || attempt == rpcAttempts-1 {
			return zero, err
		}

		timer := time.NewTimer(rpcRetryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}

	return zero, nil
}
