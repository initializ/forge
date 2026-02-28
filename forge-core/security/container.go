package security

import "os"

// InContainer returns true when the process runs inside Docker or Kubernetes.
// Used to skip the local egress proxy (NetworkPolicy enforces egress there).
func InContainer() bool {
	// Check Kubernetes: KUBERNETES_SERVICE_HOST is always set in pods
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// Check Docker: /.dockerenv is created by Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}
