package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/initializ/forge/forge-core/memory"
)

var (
	memoryMigrateDryRun bool
	memoryMigrateDir    string
)

// memoryRoot is the global memory root under ~/.forge.
func memoryRoot() string { return filepath.Join(forgeHome(), "memory") }

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Manage forge long-term memory",
	Long: `Manage forge's global long-term memory store (~/.forge/memory).

Memory is organized into two namespaces:
  projects/<project-id>   coding-session memory keyed by git remote
  agents/<agent-id>       deployed-agent operational memory keyed by agent_id`,
}

var memoryMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Copy repo-local .forge/memory into the global agents namespace",
	Long: `Copy a repo-local .forge/memory directory into the global agents
namespace at ~/.forge/memory/agents/<agent-id>/.

Daily logs (YYYY-MM-DD.md) move under sessions/. The source is left intact and
a .migrated sentinel is written so re-runs are no-ops. The agent id comes from
forge.yaml in the target directory.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		workDir := memoryMigrateDir
		if workDir == "" {
			workDir = "."
		}
		agentID := agentIDFromDir(workDir)
		source := filepath.Join(workDir, ".forge", "memory")

		res, err := memory.MigrateRepoLocal(memoryRoot(), agentID, source, memoryMigrateDryRun)
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if res.Skipped {
			fmt.Fprintf(out, "skipped: %s (%s)\n", res.Source, res.SkipNote)
			return nil
		}
		verb := "migrated"
		if res.DryRun {
			verb = "would migrate"
		}
		fmt.Fprintf(out, "%s %d file(s) from %s\n  -> %s\n", verb, len(res.Files), res.Source, res.Dest)
		for _, f := range res.Files {
			fmt.Fprintf(out, "  %s\n", f)
		}
		return nil
	},
}

var memoryProjectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List coding projects tracked in global memory",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return listRegistry(cmd, memory.NamespaceProjects)
	},
}

var memoryAgentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List deployed agents tracked in global memory",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return listRegistry(cmd, memory.NamespaceAgents)
	},
}

func listRegistry(cmd *cobra.Command, namespace string) error {
	reg, err := memory.OpenRegistry(memoryRoot(), namespace)
	if err != nil {
		return err
	}
	entries, err := reg.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no %s tracked in %s\n", namespace, memoryRoot())
		return nil
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSOURCE\tPATHS\tLAST SEEN")
	for _, e := range entries {
		last := ""
		if !e.LastSeen.IsZero() {
			last = e.LastSeen.Format("2006-01-02")
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", e.ID, e.Source, len(e.LocalPaths), last)
	}
	return tw.Flush()
}

// agentIDFromDir reads only the agent_id field from forge.yaml in dir, or ""
// when no config is found (the migrator then falls back to "unknown-agent").
// It deliberately does a lightweight parse rather than a full ParseForgeConfig
// so a partial/invalid config still yields a usable agent id for migration.
func agentIDFromDir(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		AgentID string `yaml:"agent_id"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.AgentID
}

func init() {
	memoryMigrateCmd.Flags().BoolVar(&memoryMigrateDryRun, "dry-run", false, "show what would be migrated without writing")
	memoryMigrateCmd.Flags().StringVar(&memoryMigrateDir, "dir", ".", "directory containing .forge/memory and forge.yaml")

	memoryCmd.AddCommand(memoryMigrateCmd)
	memoryCmd.AddCommand(memoryProjectsCmd)
	memoryCmd.AddCommand(memoryAgentsCmd)

	rootCmd.AddCommand(memoryCmd)
}
