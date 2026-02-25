package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/initializ/forge/forge-core/brain"
	"github.com/spf13/cobra"
)

var brainModelFlag string

var brainCmd = &cobra.Command{
	Use:   "brain",
	Short: "Manage local Brain models for offline inference",
}

var brainPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download a brain model",
	RunE:  runBrainPull,
}

var brainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available and downloaded brain models",
	RunE:  runBrainList,
}

var brainStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show brain model status",
	RunE:  runBrainStatus,
}

var brainRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a downloaded brain model",
	RunE:  runBrainRemove,
}

func init() {
	brainCmd.AddCommand(brainPullCmd)
	brainCmd.AddCommand(brainListCmd)
	brainCmd.AddCommand(brainStatusCmd)
	brainCmd.AddCommand(brainRemoveCmd)

	brainPullCmd.Flags().StringVar(&brainModelFlag, "model", "", "model ID to pull (default: auto-select)")
	brainRemoveCmd.Flags().StringVar(&brainModelFlag, "model", "", "model ID to remove")
}

func runBrainPull(_ *cobra.Command, _ []string) error {
	var model brain.ModelInfo
	if brainModelFlag != "" {
		m, ok := brain.LookupModel(brainModelFlag)
		if !ok {
			return fmt.Errorf("unknown model: %q (use 'forge brain list' to see available models)", brainModelFlag)
		}
		model = m
	} else {
		model = brain.DefaultModel()
	}

	if brain.IsModelDownloaded(model.Filename) {
		fmt.Printf("Model %q is already downloaded at %s\n", model.Name, brain.ModelPath(model.Filename))
		return nil
	}

	fmt.Printf("Downloading %s (%s)...\n", model.Name, humanSize(model.Size))

	err := brain.DownloadModel(model, func(p brain.DownloadProgress) {
		pct := float64(0)
		if p.TotalBytes > 0 {
			pct = float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
		}
		fmt.Printf("\r  %.1f%% (%s / %s)", pct, humanSize(p.DownloadedBytes), humanSize(p.TotalBytes))
	})
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	fmt.Printf("\nModel saved to %s\n", brain.ModelPath(model.Filename))
	return nil
}

func runBrainList(_ *cobra.Command, _ []string) error {
	models := brain.ListModels()

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tSIZE\tDOWNLOADED\tDEFAULT")

	for _, m := range models {
		downloaded := "no"
		if brain.IsModelDownloaded(m.Filename) {
			downloaded = "yes"
		}
		def := ""
		if m.Default {
			def = "*"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.ID, m.Name, humanSize(m.Size), downloaded, def)
	}
	_ = w.Flush()
	return nil
}

func runBrainStatus(_ *cobra.Command, _ []string) error {
	fmt.Printf("Models directory: %s\n\n", brain.ModelsDir())

	models := brain.ListModels()
	anyDownloaded := false
	for _, m := range models {
		if brain.IsModelDownloaded(m.Filename) {
			anyDownloaded = true
			path := brain.ModelPath(m.Filename)
			info, err := os.Stat(path)
			size := "unknown"
			if err == nil {
				size = humanSize(info.Size())
			}
			def := ""
			if m.Default {
				def = " (default)"
			}
			fmt.Printf("  %s%s\n", m.Name, def)
			fmt.Printf("    Path: %s\n", path)
			fmt.Printf("    Size: %s\n", size)
		}
	}

	if !anyDownloaded {
		fmt.Println("No brain models downloaded.")
		fmt.Println("Run 'forge brain pull' to download the default model.")
	}

	return nil
}

func runBrainRemove(_ *cobra.Command, _ []string) error {
	modelID := brainModelFlag
	if modelID == "" {
		modelID = brain.DefaultModel().ID
	}

	model, ok := brain.LookupModel(modelID)
	if !ok {
		return fmt.Errorf("unknown model: %q", modelID)
	}

	if !brain.IsModelDownloaded(model.Filename) {
		return fmt.Errorf("model %q is not downloaded", model.Name)
	}

	if err := brain.RemoveModel(model.Filename); err != nil {
		return fmt.Errorf("removing model: %w", err)
	}

	fmt.Printf("Removed model %q\n", model.Name)
	return nil
}

func humanSize(bytes int64) string {
	const (
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.0f MB", float64(bytes)/float64(mb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
