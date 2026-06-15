package scheduler

import "os"

// inClusterServiceAccountTokenPath is the standard mount point a
// kubelet injects into every pod's filesystem when the pod has a
// ServiceAccount. Presence is the canonical Kubernetes-supplied
// "you are running inside a pod" signal.
const inClusterServiceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// InCluster reports whether the process appears to be running inside
// a Kubernetes pod. The signal is the presence of the projected
// ServiceAccount token at the well-known mount path. Override at test
// time by setting the FORGE_IN_CLUSTER env var ("true" / "false") —
// useful for unit tests on developer laptops and for forcing
// file-backend behavior inside a cluster (e.g. single-replica dev
// deploys that don't want CronJob CRUD).
func InCluster() bool {
	if v := os.Getenv("FORGE_IN_CLUSTER"); v != "" {
		return v == "true" || v == "1"
	}
	_, err := os.Stat(inClusterServiceAccountTokenPath)
	return err == nil
}
