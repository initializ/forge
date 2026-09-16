package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/optimizer"
)

var (
	optimizerBenchModel   string
	optimizerBenchPricing string
	optimizerBenchJSON    bool
)

var optimizerBenchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Benchmark compression on representative payloads (ratios + dollars, no API calls)",
	Long: `Runs a set of representative tool-output payloads (JSON, logs, search dumps,
code) through the real compression pipeline and reports the compression ratio,
tokens saved, and cost avoided for each — entirely offline, no API calls. Use it
to sanity-check what the optimizer will save on your kind of content before
wiring it into a live session.`,
	RunE: runOptimizerBench,
}

func init() {
	optimizerBenchCmd.Flags().StringVar(&optimizerBenchModel, "model", "claude-opus-4-8", "model id to price savings against")
	optimizerBenchCmd.Flags().StringVar(&optimizerBenchPricing, "pricing-file", defaultPricingFile, "JSON per-model price overrides")
	optimizerBenchCmd.Flags().BoolVar(&optimizerBenchJSON, "json", false, "print raw JSON")
	optimizerCmd.AddCommand(optimizerBenchCmd)
}

type benchResult struct {
	Name         string  `json:"name"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	Saved        int     `json:"saved_tokens"`
	Ratio        float64 `json:"ratio"`
	BytesBefore  int     `json:"bytes_before"`
	BytesAfter   int     `json:"bytes_after"`
	Dollars      float64 `json:"cost_avoided_usd"`
}

func runOptimizerBench(_ *cobra.Command, _ []string) error {
	overrides, err := optimizer.LoadPricingFile(firstNonEmpty(optimizerBenchPricing, defaultPricingFile))
	if err != nil {
		return fmt.Errorf("reading pricing file: %w", err)
	}
	pricing := optimizer.NewPricing(overrides)

	tmp, err := os.MkdirTemp("", "forge-optimizer-bench")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	comp, err := optimizer.NewCompressor(optimizer.CompressConfig{
		StorePath: filepath.Join(tmp, "bench.db"),
		MinTokens: 30,
	})
	if err != nil {
		return err
	}
	defer func() { _ = comp.Close() }()

	samples := benchSamples()
	results := make([]benchResult, 0, len(samples))
	var totBefore, totAfter, totSaved int
	var totDollars float64

	for _, s := range samples {
		body := benchBody(optimizerBenchModel, s.content)
		out, st, _ := comp.Transform(body)
		dollars := pricing.CostAvoided(optimizerBenchModel, int64(st.SavedTokens))
		ratio := 0.0
		if st.TokensBefore > 0 {
			ratio = float64(st.SavedTokens) / float64(st.TokensBefore)
		}
		results = append(results, benchResult{
			Name:         s.name,
			TokensBefore: st.TokensBefore,
			TokensAfter:  st.TokensAfter,
			Saved:        st.SavedTokens,
			Ratio:        ratio,
			BytesBefore:  len(body),
			BytesAfter:   len(out),
			Dollars:      dollars,
		})
		totBefore += st.TokensBefore
		totAfter += st.TokensAfter
		totSaved += st.SavedTokens
		totDollars += dollars
	}

	if optimizerBenchJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"model": optimizerBenchModel, "results": results,
			"total": map[string]any{"tokens_before": totBefore, "tokens_after": totAfter, "saved": totSaved, "cost_avoided_usd": totDollars}})
	}

	fmt.Printf("\nforge optimizer — compression benchmark (priced at %s)\n\n", optimizerBenchModel)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PAYLOAD\tTOKENS\tSAVED\tRATIO\tBYTES\t$ AVOIDED")
	for _, r := range results {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%5.1f%%\t%s→%s\t$%.4f\n",
			r.Name, commaInt(int64(r.TokensBefore)), commaInt(int64(r.Saved)), r.Ratio*100,
			commaInt(int64(r.BytesBefore)), commaInt(int64(r.BytesAfter)), r.Dollars)
	}
	totRatio := 0.0
	if totBefore > 0 {
		totRatio = float64(totSaved) / float64(totBefore)
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%5.1f%%\t\t$%.4f\n", "TOTAL",
		commaInt(int64(totBefore)), commaInt(int64(totSaved)), totRatio*100, totDollars)
	_ = tw.Flush()
	fmt.Println("\nctxzip targets structured/repetitive output (JSON, logs, search dumps); prose and code compress less.")
	return nil
}

type benchSample struct {
	name    string
	content string
}

// benchBody wraps a sample as a tool_result in a minimal /v1/messages request
// so it flows through the real compression path (frozen anchor + live zone).
func benchBody(model, sample string) []byte {
	req := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze the tool output"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_bench", "content": sample},
			}},
			map[string]any{"role": "user", "content": "?"},
		},
	}
	b, _ := json.Marshal(req)
	return b
}

func benchSamples() []benchSample {
	// JSON array of records — the SmartCrusher sweet spot.
	var jsonArr strings.Builder
	jsonArr.WriteString("[")
	for i := 0; i < 600; i++ {
		if i > 0 {
			jsonArr.WriteString(",")
		}
		fmt.Fprintf(&jsonArr, `{"id":%d,"level":"info","service":"api","status":200,"msg":"request %d handled ok"}`, i, i)
	}
	jsonArr.WriteString("]")

	// Structured log entries (JSON array) with a buried error that must survive.
	var logs strings.Builder
	logs.WriteString("[")
	for i := 0; i < 500; i++ {
		if i > 0 {
			logs.WriteString(",")
		}
		if i == 267 {
			logs.WriteString(`{"ts":"2026-09-10T14:00:00Z","level":"FATAL","msg":"db connection pool exhausted after 30s"}`)
			continue
		}
		fmt.Fprintf(&logs, `{"ts":"2026-09-10T14:00:%02dZ","level":"INFO","worker":%d,"msg":"processed batch, 0 errors"}`, i%60, i%8)
	}
	logs.WriteString("]")

	// Search results (JSON array of matches — as a code-search tool returns).
	var search strings.Builder
	search.WriteString("[")
	for i := 0; i < 400; i++ {
		if i > 0 {
			search.WriteString(",")
		}
		fmt.Fprintf(&search, `{"file":"src/pkg/module_%d.go","line":%d,"match":"func Handle%d(ctx context.Context) error"}`, i%20, i, i)
	}
	search.WriteString("]")

	return []benchSample{
		{"json-records (600)", jsonArr.String()},
		{"json-logs (500, 1 FATAL)", logs.String()},
		{"search-results (400)", search.String()},
	}
}
