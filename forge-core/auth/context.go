package auth

import "context"

// identityKey is an unexported type used as the context key, so that no
// other package can collide with our value.
type identityKey struct{}

// WithIdentity returns a copy of ctx that carries the given Identity.
// Storing nil is a no-op (returns ctx unchanged).
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the Identity stored on ctx by WithIdentity,
// or nil if no Identity is present.
func IdentityFromContext(ctx context.Context) *Identity {
	if ctx == nil {
		return nil
	}
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}
