package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/compress"
)

var compressionCmd = &cobra.Command{
	Use:   "compression",
	Short: "Inspect context compression state",
}

var compressionSuggestionsCmd = &cobra.Command{
	Use:   "suggestions",
	Short: "Show keep_patterns candidates mined from context_expand retrievals",
	Long: `Every context_expand retrieval means compression dropped something a model
needed. The runtime mines retrieved content for domain-state tokens
(CamelCase / ALLCAPS words not already protected by the error floor or
keep_patterns) and counts them across expansions.

This command renders the accumulated candidates plus a paste-ready
compression.keep_patterns block for forge.yaml. Suggestions are advisory —
review before adopting; a token retrieved often is evidence, not proof.`,
	RunE: compressionSuggestionsRun,
}

func init() {
	compressionCmd.AddCommand(compressionSuggestionsCmd)
}

func compressionSuggestionsRun(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadAndPrepareConfig(".env")
	if err != nil {
		return err
	}

	storePath := cfg.Compression.StorePath
	if storePath == "" {
		storePath = filepath.Join(workDir, ".forge", "ctxzip.db")
	}
	path := compress.SuggestionsPath(storePath)

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No suggestions yet — the flywheel records candidates when models retrieve compressed content via context_expand.")
			return nil
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var stats []compress.PatternStat
	if err := json.Unmarshal(raw, &stats); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(stats) == 0 {
		fmt.Println("No suggestions yet.")
		return nil
	}
	sort.Slice(stats, func(a, b int) bool {
		if stats[a].Expansions != stats[b].Expansions {
			return stats[a].Expansions > stats[b].Expansions
		}
		return stats[a].Pattern < stats[b].Pattern
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "PATTERN\tEXPANSIONS\tTOOLS\tSUGGESTED\n")
	for _, st := range stats {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%v\n",
			st.Pattern, st.Expansions, strings.Join(st.Tools, ","), st.Suggested)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Paste-ready block for the entries that crossed the threshold.
	var suggested []compress.PatternStat
	for _, st := range stats {
		if st.Suggested {
			suggested = append(suggested, st)
		}
	}
	if len(suggested) > 0 {
		fmt.Println("\n# Candidates that crossed the suggestion threshold — review, then")
		fmt.Println("# merge into forge.yaml:")
		fmt.Println("compression:")
		fmt.Println("  keep_patterns:")
		for _, st := range suggested {
			fmt.Printf("    - %s   # retrieved in %d expansions (%s)\n",
				st.Pattern, st.Expansions, strings.Join(st.Tools, ","))
		}
	}
	return nil
}
