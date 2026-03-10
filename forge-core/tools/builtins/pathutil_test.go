package builtins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathValidator_Resolve(t *testing.T) {
	workDir := t.TempDir()

	pv := NewPathValidator(workDir)

	tests := []struct {
		name    string
		path    string
		wantErr bool
		wantAbs string // expected absolute path (empty = just check no error)
	}{
		{
			name:    "empty path returns workDir",
			path:    "",
			wantAbs: workDir,
		},
		{
			name:    "relative path",
			path:    "foo/bar.txt",
			wantAbs: filepath.Join(workDir, "foo", "bar.txt"),
		},
		{
			name:    "absolute path within workDir",
			path:    filepath.Join(workDir, "src", "main.go"),
			wantAbs: filepath.Join(workDir, "src", "main.go"),
		},
		{
			name:    "dot path returns workDir",
			path:    ".",
			wantAbs: workDir,
		},
		{
			name:    "path traversal blocked",
			path:    "../../../etc/passwd",
			wantErr: true,
		},
		{
			name:    "absolute path outside workDir blocked",
			path:    "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "sneaky traversal blocked",
			path:    "foo/../../..",
			wantErr: true,
		},
		{
			name:    "dot-dot in middle resolved safely",
			path:    "foo/../bar.txt",
			wantAbs: filepath.Join(workDir, "bar.txt"),
		},
	}

	// Test workspace/ fallback: when "myrepo" doesn't exist but "workspace/myrepo" does,
	// Resolve should return the workspace path.
	t.Run("workspace fallback", func(t *testing.T) {
		wsDir := filepath.Join(workDir, "workspace", "myrepo")
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := pv.Resolve("myrepo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != wsDir {
			t.Errorf("got %q, want %q (workspace fallback)", got, wsDir)
		}

		// "workspace/myrepo" should also work directly
		got2, err2 := pv.Resolve("workspace/myrepo")
		if err2 != nil {
			t.Fatalf("unexpected error: %v", err2)
		}
		if got2 != wsDir {
			t.Errorf("got %q, want %q (direct workspace path)", got2, wsDir)
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pv.Resolve(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantAbs != "" && got != tt.wantAbs {
				t.Errorf("got %q, want %q", got, tt.wantAbs)
			}
		})
	}
}
