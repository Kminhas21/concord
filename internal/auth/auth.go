// Package auth is the daemon's team-token gate on the RPC (ADR-0011). It is a
// Connect interceptor, installed outermost, so a rejected call never reaches
// coordination. The health probes are plain HTTP handlers outside the Connect
// chain and are never gated.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

var errRejected = errors.New("concord: invalid or missing team token")

// Interceptor requires "Authorization: Bearer <token>" on every RPC and answers
// CodeUnauthenticated otherwise. onReject (may be nil) is called once per
// rejection; it must never be handed the presented credential.
func Interceptor(token string, onReject func(ctx context.Context)) connect.Interceptor {
	want := []byte(token)
	check := func(ctx context.Context, h http.Header) error {
		got, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
			return nil
		}
		if onReject != nil {
			onReject(ctx)
		}
		return connect.NewError(connect.CodeUnauthenticated, errRejected)
	}
	return interceptor{check}
}

type interceptor struct {
	check func(context.Context, http.Header) error
}

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.check(ctx, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// Server-side streaming handlers are checked too, so a future streaming RPC
// cannot bypass auth. The client-side wrap is a pass-through (never used here).
func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.check(ctx, conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
