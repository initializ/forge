package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/export"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
)

var (
	exportOutput         string
	exportPretty         bool
	exportIncludeSchemas bool
	exportSimulateImport bool
	exportDevMode        bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export the agent specification for Command platform import",
	Long:  "Export produces a standalone AgentSpec JSON file with metadata for importing into the Command platform.",
	RunE:  runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportOutput, "output", "", "output file path (default: {agent_id}-forge.json)")
	exportCmd.Flags().BoolVar(&exportPretty, "pretty", false, "format JSON with indentation")
	exportCmd.Flags().BoolVar(&exportIncludeSchemas, "include-schemas", false, "embed tool schemas inline from build output")
	exportCmd.Flags().BoolVar(&exportSimulateImport, "simulate-import", false, "print simulated Command import result to stdout")
	exportCmd.Flags().BoolVar(&exportDevMode, "dev", false, "include dev-category tools in export")
}

func runExport(cmd *cobra.Command, args []string) error {
	// 1. Resolve config path
	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		cfgPath = filepath.Join(wd, cfgPath)
	}

	// 2. Load config (needed for agent_id default filename)
	cfg, err := config.LoadForgeConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// 3. Determine output dir and ensure build output exists
	outDir := outputDir
	if outDir == "." {
		outDir = filepath.Join(filepath.Dir(cfgPath), ".forge-output")
	}

	if err := ensureBuildOutput(outDir, cfgPath); err != nil {
		return err
	}

	// 4. Read agent.json from build output
	agentJSONPath := filepath.Join(outDir, "agent.json")
	agentData, err := os.ReadFile(agentJSONPath)
	if err != nil {
		return fmt.Errorf("reading agent.json: %w", err)
	}

	// 5. Validate agent.json against schema
	schemaErrs, err := validate.ValidateAgentSpec(agentData)
	if err != nil {
		return fmt.Errorf("validating agent.json: %w", err)
	}
	if len(schemaErrs) > 0 {
		for _, e := range schemaErrs {
			fmt.Fprintf(os.Stderr, "ERROR: agent.json: %s\n", e)
		}
		return fmt.Errorf("agent.json schema validation failed: %d error(s)", len(schemaErrs))
	}

	// 6. Unmarshal into AgentSpec
	var spec agentspec.AgentSpec
	if err := json.Unmarshal(agentData, &spec); err != nil {
		return fmt.Errorf("parsing agent.json: %w", err)
	}

	// 6a. Export validation
	exportVal := export.ValidateForExport(&spec, exportDevMode)
	for _, w := range exportVal.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	if len(exportVal.Errors) > 0 {
		for _, e := range exportVal.Errors {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
		}
		return fmt.Errorf("export validation failed: %s", exportVal.Errors[0])
	}

	// 7. Run Command compatibility validation
	compatResult := validate.ValidateCommandCompat(&spec)
	for _, w := range compatResult.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	for _, e := range compatResult.Errors {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
	}
	if !compatResult.IsValid() {
		return fmt.Errorf("Command compatibility check failed: %d error(s)", len(compatResult.Errors))
	}

	// 8. Include schemas if requested
	if exportIncludeSchemas {
		if err := embedToolSchemas(outDir, &spec); err != nil {
			return fmt.Errorf("embedding tool schemas: %w", err)
		}
	}

	// 9. Simulate import mode
	if exportSimulateImport {
		simResult := validate.SimulateImport(&spec)
		var simData []byte
		simData, err = json.MarshalIndent(simResult, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling import simulation: %w", err)
		}
		fmt.Println(string(simData))
		return nil
	}

	// 10. Read allowlist domains from build output
	var allowlistDomains []string
	allowlistPath := filepath.Join(outDir, "compiled", "egress_allowlist.json")
	if data, readErr := os.ReadFile(allowlistPath); readErr == nil {
		var allowlist map[string]any
		if json.Unmarshal(data, &allowlist) == nil {
			if domains, ok := allowlist["all_domains"].([]any); ok {
				for _, d := range domains {
					if s, ok := d.(string); ok {
						allowlistDomains = append(allowlistDomains, s)
					}
				}
			}
		}
	}

	// 11. Build export envelope
	envelope, err := export.BuildEnvelope(&spec, allowlistDomains, appVersion)
	if err != nil {
		return fmt.Errorf("building export envelope: %w", err)
	}

	// 12. Marshal final output
	var exportData []byte
	if exportPretty {
		exportData, err = json.MarshalIndent(envelope, "", "  ")
	} else {
		exportData, err = json.Marshal(envelope)
	}
	if err != nil {
		return fmt.Errorf("marshalling export: %w", err)
	}
	exportData = append(exportData, '\n')

	// 13. Determine output filename
	outFile := exportOutput
	if outFile == "" {
		outFile = fmt.Sprintf("%s-forge.json", cfg.AgentID)
	}

	if err := os.WriteFile(outFile, exportData, 0644); err != nil {
		return fmt.Errorf("writing export file: %w", err)
	}

	fmt.Printf("Exported: %s\n", outFile)
	return nil
}

// embedToolSchemas reads tool schema files from .forge-output/tools/ and
// merges them into the spec's tool InputSchema fields.
func embedToolSchemas(outDir string, spec *agentspec.AgentSpec) error {
	toolsDir := filepath.Join(outDir, "tools")
	for i := range spec.Tools {
		schemaFile := filepath.Join(toolsDir, spec.Tools[i].Name+".schema.json")
		data, err := os.ReadFile(schemaFile)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("reading schema for tool %s: %w", spec.Tools[i].Name, err)
		}
		// Validate it's valid JSON
		var js json.RawMessage
		if err := json.Unmarshal(data, &js); err != nil {
			return fmt.Errorf("invalid JSON in schema for tool %s: %w", spec.Tools[i].Name, err)
		}
		spec.Tools[i].InputSchema = js
	}
	return nil
}
