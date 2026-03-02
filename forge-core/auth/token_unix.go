//go:build !windows

package auth

// setFileOwnerOnly is a no-op on Unix — os.WriteFile with 0600 is sufficient.
func setFileOwnerOnly(_ string) error {
	return nil
}
