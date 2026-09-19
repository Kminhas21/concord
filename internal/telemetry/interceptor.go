package telemetry

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
)

// attrString is a small helper to keep instrument call sites terse.
func attrString(k, v string) attribute.KeyValue { return attribute.String(k, v) }

// Interceptor returns a Connect unary interceptor that records each RPC's
// handler latency into concord_rpc_duration_seconds{method}. It only observes;
// it never alters the request, response, or error.
func Interceptor(m *Metrics) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)
			m.recordRPC(ctx, methodName(req.Spec().Procedure), time.Since(start))
			return resp, err
		}
	}
}

// methodName reduces a Connect procedure path
// ("/concord.v1.CoordinationService/CheckEdit") to its method ("CheckEdit").
func methodName(procedure string) string {
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		return procedure[i+1:]
	}
	return procedure
}
