package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/optimizer"
)

var (
	optimizerSavingsUsageLog string
	optimizerSavingsPricing  string
	optimizerSavingsJSON     bool
)

// defaultPricingFile is the optional per-model price override the savings/bench
// views read, in the shared ~/.forge directory. Enterprises drop negotiated
// rates here.
var defaultPricingFile = forgeFile("optimizer-pricing.json")

var optimizerSavingsCmd = &cobra.Command{
	Use:   "savings",
	Short: "Show token + dollar savings over time (today / 7d / 30d), per model and per client",
	Long: `Reads the optimizer's local usage log and reports compression savings in
tokens and dollars across time windows, plus a per-model cost-avoided breakdown
and a per-client summary.

Dollar figures use public list prices by default. If your organization has
negotiated rates, drop them in ` + defaultPricingFile + ` (or pass --pricing-file):

  { "claude-opus-4-8": { "input_per_mtok": 4.0, "output_per_mtok": 20.0 },
    "unknown":         { "input_per_mtok": 2.5, "output_per_mtok": 12.0 } }`,
	RunE: runOptimizerSavings,
}

func init() {
	optimizerSavingsCmd.Flags().StringVar(&optimizerSavingsUsageLog, "usage-log", defaultUsageLog, "NDJSON usage log to read")
	optimizerSavingsCmd.Flags().StringVar(&optimizerSavingsPricing, "pricing-file", defaultPricingFile, "JSON per-model price overrides (negotiated rates)")
	optimizerSavingsCmd.Flags().BoolVar(&optimizerSavingsJSON, "json", false, "print raw JSON")
	optimizerCmd.AddCommand(optimizerSavingsCmd)
}

func runOptimizerSavings(_ *cobra.Command, _ []string) error {
	overrides, err := optimizer.LoadPricingFile(firstNonEmpty(optimizerSavingsPricing, defaultPricingFile))
	if err != nil {
		return fmt.Errorf("reading pricing file: %w", err)
	}
	pricing := optimizer.NewPricing(overrides)

	logPath := firstNonEmpty(optimizerSavingsUsageLog, defaultUsageLog)
	// One aggregator shared with the forge-ui dashboard, so numbers match.
	report, err := optimizer.AggregateUsageLog(logPath, pricing, time.Now(), 0)
	if err != nil {
		return err
	}
	if report.Records == 0 {
		return fmt.Errorf("no usage recorded in %s — run `forge optimizer --compress` first", logPath)
	}

	if optimizerSavingsJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	renderSavings(report)
	return nil
}

func renderSavings(r *optimizer.UsageReport) {
	fmt.Println()
	printWindow("Today       ", r.Today)
	printWindow("Last 7 days ", r.Last7Days)
	printWindow("Last 30 days", r.Last30Days)

	if len(r.PerModel) > 0 {
		fmt.Println("\nCost avoided per model:")
		models := make([]string, 0, len(r.PerModel))
		for k := range r.PerModel {
			models = append(models, k)
		}
		sort.Slice(models, func(i, j int) bool { return r.PerModel[models[i]].Dollars > r.PerModel[models[j]].Dollars })
		for _, m := range models {
			fmt.Printf("  %-24s $%.2f\n", m, r.PerModel[m].Dollars)
		}
	}

	if len(r.PerClient) > 0 {
		fmt.Println("\nSavings by client:")
		clients := make([]string, 0, len(r.PerClient))
		for k := range r.PerClient {
			clients = append(clients, k)
		}
		sort.Slice(clients, func(i, j int) bool { return r.PerClient[clients[i]].SavedTokens > r.PerClient[clients[j]].SavedTokens })
		for _, c := range clients {
			fmt.Printf("  %-24s %d calls · %s tokens saved\n", c, r.PerClient[c].Calls, commaInt(r.PerClient[c].SavedTokens))
		}
	}
	fmt.Println()
}

func printWindow(label string, w optimizer.WindowTotals) {
	pct := func(num, den int64) float64 {
		if den <= 0 {
			return 0
		}
		r := float64(num) / float64(den) * 100
		if r > 100 {
			r = 100 // guard against any residual mixed-record skew
		}
		return r
	}
	// Ratio = saved / cache-write tokens (freshly-cached bytes each turn) — the
	// share of newly-cached token volume compression removed. The $ figure is the
	// full three-tier cost avoided: uncached input (1×) + cache-write (1.25×) the
	// content would have been cached at, + the compounding cache-read (0.1×) it
	// avoids on every later turn of the session.
	ratio := pct(w.SavedTokens, w.CacheWriteTokens)
	fmt.Printf("%s %s  %5.1f%% saved %s / %s (cache write)   $%.2f\n",
		label, bar(ratio/100, 15), ratio, commaInt(w.SavedTokens), commaInt(w.CacheWriteTokens), w.Dollars)
	fmt.Printf("               ↳ avoided  input %s ·  cache-write %s ·  cache-read %s (×0.1, compounding)\n",
		commaInt(w.AvoidedInputTokens), commaInt(w.AvoidedCacheWriteTokens), commaInt(w.AvoidedCacheReadTokens))
}

// bar renders a ratio in [0,1] as a filled/empty block bar of the given width.
func bar(ratio float64, width int) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// commaInt formats an int64 with thousands separators.
func commaInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(c)
	}
	if neg {
		return "-" + out.String()
	}
	return out.String()
}
