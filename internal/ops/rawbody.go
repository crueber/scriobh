package ops

import (
	"context"
	"io"
)

// RawBody is the stream behind an op registered with Op.RawBody: the bytes the
// client sent, the type it claimed (never trusted) and the length it declared,
// or -1. The REST binding sets it after authorization and before the handler
// runs; the handler reads it once.
type RawBody struct {
	R           io.Reader
	ContentType string
	Length      int64
}

type rawBodyKey struct{}

// WithRawBody attaches a raw body to a context for Registry.Call.
func WithRawBody(ctx context.Context, b RawBody) context.Context {
	return context.WithValue(ctx, rawBodyKey{}, b)
}

// RawBody returns the request body of a raw-body op.
func (c *Ctx) RawBody() (RawBody, bool) {
	b, ok := c.Context.Value(rawBodyKey{}).(RawBody)
	return b, ok && b.R != nil
}
