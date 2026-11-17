package builtins_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
)

func TestGrepSearchOutcomes(t *testing.T) {
	rgPath, rgErr := exec.LookPath("rg")
	for _, backend := range []string{"ripgrep", "go"} {
		t.Run(backend, func(t *testing.T) {
			if backend == "ripgrep" {
				if rgErr != nil {
					t.Skip("ripgrep is not installed")
				}
				t.Setenv("PATH", filepath.Dir(rgPath))
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			t.Setenv("RIPGREP_CONFIG_PATH", "")

			workDir := t.TempDir()
			for name, content := range map[string]string{
				"pods.json":                "Running\n",
				"empty.txt":                "",
				"binary.bin":               "\x00binary\n",
				"filters/pods.json":        "Running\n",
				"filters/pods.test.json":   "Failed\n",
				"nested/child/pods.json":   "Running\n",
				"hidden/.pods.json":        "Running\n",
				"ignored/.ignore":          "pods.json\n",
				"ignored/pods.json":        "Running\n",
				"skipped/vendor/pods.json": "Running\n",
				"workspace/repo/pods.json": "Running\n",
				"workspace/pods.json":      "Failed\n",
			} {
				path := filepath.Join(workDir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(workDir, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}

			var search tools.Tool
			for _, tool := range builtins.CodeAgentSearchTools(workDir) {
				if tool.Name() == "grep_search" {
					search = tool
				}
			}
			if search == nil {
				t.Fatal("grep_search is missing from CodeAgentSearchTools")
			}

			missingAbsolute := filepath.Join(workDir, "missing.json")
			cases := []struct {
				name, path, pattern, include, exclude, want, wantRipgrep string
			}{
				{name: "missing file", path: "missing.json", pattern: "Error", want: "(path not found: missing.json)"},
				{name: "missing directory", path: "missing/", pattern: "Error", want: "(path not found: missing/)"},
				{name: "missing absolute path", path: missingAbsolute, pattern: "Error", want: "(path not found: " + missingAbsolute + ")"},
				{name: "missing before include", path: "missing.json", pattern: "Error", include: "*.absent", want: "(path not found: missing.json)"},
				{name: "unmatched include", path: ".", pattern: "Error", include: "*.absent", want: "(no files matched include pattern)"},
				{name: "default path", pattern: "Error", include: "*.absent", want: "(no files matched include pattern)"},
				{name: "true no match", path: "pods.json", pattern: "Error", want: "(no matches found)"},
				{name: "positive file", path: "pods.json", pattern: "Running", want: "pods.json:1:Running\n", wantRipgrep: "1:Running\n"},
				{name: "selected no match", path: "filters", pattern: "Error", include: "*.json", want: "(no matches found)"},
				{name: "selected empty file", include: "empty.txt", pattern: "Error", want: "(no matches found)"},
				{name: "selected binary file", include: "*.bin", pattern: "Error", want: "(no matches found)"},
				{name: "empty directory with include", path: "empty", include: "*.json", pattern: "Error", want: "(no files matched include pattern)"},
				{name: "empty directory", path: "empty", pattern: "Error", want: "(no matches found)"},
				{name: "include and exclude", path: "filters", pattern: "Running|Failed", include: "*.json", exclude: "*.test.json", want: filepath.Join("filters", "pods.json") + ":1:Running\n"},
				{name: "exclude all selected files", path: "filters", pattern: "Running", include: "*.json", exclude: "*.json", want: "(no files matched include pattern)"},
				{name: "exclude without include", path: "filters", pattern: "Running", exclude: "*.json", want: "(no matches found)"},
				{name: "nested include", path: "nested", pattern: "Running", include: "*.json", want: filepath.Join("nested", "child", "pods.json") + ":1:Running\n"},
				{name: "path glob no match", path: "nested", pattern: "Error", include: "**/child/*.json", want: "(no files matched include pattern)", wantRipgrep: "(no matches found)"},
				{name: "path glob positive", path: "nested", pattern: "Running", include: "**/child/*.json", want: "(no files matched include pattern)", wantRipgrep: filepath.Join("nested", "child", "pods.json") + ":1:Running\n"},
				{name: "explicit file glob", path: "pods.json", pattern: "Error", include: "*.absent", want: "(no files matched include pattern)", wantRipgrep: "(no matches found)"},
				{name: "hidden default", path: "hidden", pattern: "Running", want: filepath.Join("hidden", ".pods.json") + ":1:Running\n", wantRipgrep: "(no matches found)"},
				{name: "hidden include", path: "hidden", pattern: "Running", include: "*.json", want: filepath.Join("hidden", ".pods.json") + ":1:Running\n"},
				{name: "ignore default", path: "ignored", pattern: "Running", want: filepath.Join("ignored", "pods.json") + ":1:Running\n", wantRipgrep: "(no matches found)"},
				{name: "ignore include", path: "ignored", pattern: "Running", include: "*.json", want: filepath.Join("ignored", "pods.json") + ":1:Running\n"},
				{name: "skipped directory", path: "skipped", pattern: "Error", include: "*.json", want: "(no files matched include pattern)", wantRipgrep: "(no matches found)"},
				{name: "workspace file fallback", path: "repo/pods.json", pattern: "Running", want: filepath.Join("workspace", "repo", "pods.json") + ":1:Running\n", wantRipgrep: "1:Running\n"},
				{name: "workspace directory fallback", path: "repo", pattern: "Running", include: "*.json", want: filepath.Join("workspace", "repo", "pods.json") + ":1:Running\n"},
				{name: "workspace missing file", path: "repo/missing.json", pattern: "Error", want: "(path not found: repo/missing.json)"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					args, err := json.Marshal(map[string]string{
						"path": tc.path, "pattern": tc.pattern, "include": tc.include, "exclude": tc.exclude,
					})
					if err != nil {
						t.Fatal(err)
					}
					result, err := search.Execute(context.Background(), args)
					if err != nil {
						t.Fatalf("Execute() error = %v", err)
					}
					want := tc.want
					if backend == "ripgrep" && tc.wantRipgrep != "" {
						want = tc.wantRipgrep
					}
					if result != want {
						t.Errorf("Execute() = %q, want %q", result, want)
					}
				})
			}

			for _, path := range []string{"../outside.json", filepath.Join(filepath.Dir(workDir), "outside.json")} {
				t.Run("confined/"+path, func(t *testing.T) {
					args, err := json.Marshal(map[string]string{"path": path, "pattern": "Error"})
					if err != nil {
						t.Fatal(err)
					}
					result, err := search.Execute(context.Background(), args)
					if result != "" || err == nil || !strings.Contains(err.Error(), "outside the working directory") {
						t.Fatalf("Execute() = %q, %v; want a confinement error", result, err)
					}
				})
			}
		})
	}
}
