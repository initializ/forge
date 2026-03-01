package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/spf13/cobra"
)

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Manage agent cron schedules",
}

var scheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured schedules",
	RunE:  scheduleListRun,
}

func init() {
	scheduleCmd.AddCommand(scheduleListCmd)
}

func scheduleListRun(cmd *cobra.Command, args []string) error {
	workDir, _ := os.Getwd()
	schedPath := filepath.Join(workDir, ".forge", "memory", "SCHEDULES.md")

	store := runtime.NewMemoryScheduleStore(schedPath)
	ctx := context.Background()

	schedules, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("reading schedules: %w", err)
	}

	if len(schedules) == 0 {
		fmt.Println("No schedules configured.")
		return nil
	}

	now := time.Now().UTC()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "ID\tCRON\tSOURCE\tENABLED\tNEXT FIRE\tTASK\n")

	for _, sched := range schedules {
		nextFire := "N/A"
		if sched.Enabled {
			parsed, parseErr := scheduler.Parse(sched.Cron)
			if parseErr == nil {
				ref := sched.LastRun
				if ref.IsZero() {
					ref = now
				}
				next := parsed.Next(ref)
				if !next.IsZero() {
					nextFire = next.Format(time.RFC3339)
				}
			}
		}

		task := sched.Task
		if len(task) > 50 {
			task = task[:47] + "..."
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\n",
			sched.ID, sched.Cron, sched.Source, sched.Enabled, nextFire, task)
	}

	return w.Flush()
}
