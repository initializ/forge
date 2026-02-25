//go:build !brain

package brain

// newEngine returns ErrBrainNotCompiled when built without the brain tag.
func newEngine(_ Config) (engine, error) {
	return nil, ErrBrainNotCompiled
}
