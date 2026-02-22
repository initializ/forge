package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/container"
	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/export"
	"github.com/initializ/forge/forge-core/types"
	"github.com/spf13/cobra"
)

var (
	pushImage    bool
	platform     string
	noCache      bool
	devMode      bool
	prodMode     bool
	verifyFlag   bool
	registry     string
	builderArg   string
	skipBuild    bool
	withChannels bool
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Build a container image for the agent",
	Long:  "Package builds a container image from the forge build output. It auto-detects docker/podman/buildah.",
	RunE:  runPackage,
}

func init() {
	packageCmd.Flags().BoolVar(&pushImage, "push", false, "push image to registry after building")
	packageCmd.Flags().StringVar(&platform, "platform", "", "target platform (e.g., linux/amd64)")
	packageCmd.Flags().BoolVar(&noCache, "no-cache", false, "disable layer cache")
	packageCmd.Flags().BoolVar(&devMode, "dev", false, "include dev tools in image")
	packageCmd.Flags().BoolVar(&prodMode, "prod", false, "production build: reject dev tools and dev-open egress")
	packageCmd.Flags().BoolVar(&verifyFlag, "verify", false, "smoke-test container after build")
	packageCmd.Flags().StringVar(&registry, "registry", "", "registry prefix (e.g., ghcr.io/org)")
	packageCmd.Flags().StringVar(&builderArg, "builder", "", "force specific builder (docker, podman, buildah)")
	packageCmd.Flags().BoolVar(&skipBuild, "skip-build", false, "skip re-running forge build")
	packageCmd.Flags().BoolVar(&withChannels, "with-channels", false, "generate docker-compose.yaml with channel adapters")
}


func runPackage(cmd *cobra.Command, args []string) error {
	// Mutual exclusivity check
	if devMode && prodMode {
		return fmt.Errorf("--dev and --prod flags are mutually exclusive")
	}

	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		cfgPath = filepath.Join(wd, cfgPath)
	}

	cfg, err := config.LoadForgeConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Production validation
	if prodMode {
		if err := validateProdConfig(cfg); err != nil {
			return fmt.Errorf("production validation failed: %w", err)
		}
	}

	outDir := outputDir
	if outDir == "." {
		outDir = filepath.Join(filepath.Dir(cfgPath), ".forge-output")
	}

	// Use registry from flag, fall back to config
	reg := registry
	if reg == "" {
		reg = cfg.Registry
	}

	// Check if build output exists and is fresh
	if !skipBuild {
		if err := ensureBuildOutput(outDir, cfgPath); err != nil {
			return err
		}
	} else {
		// Even with --skip-build, the output dir must exist
		if _, err := os.Stat(filepath.Join(outDir, "build-manifest.json")); os.IsNotExist(err) {
			return fmt.Errorf("build output not found at %s; run 'forge build' first or remove --skip-build", outDir)
		}
	}

	// Detect or select builder
	var builder container.Builder
	if builderArg != "" {
		builder = container.Get(builderArg)
		if builder == nil {
			return fmt.Errorf("unknown builder: %s (supported: docker, podman, buildah)", builderArg)
		}
		if !builder.Available() {
			return fmt.Errorf("builder %s is not available; ensure it is installed and running", builderArg)
		}
	} else {
		builder = container.Detect()
		if builder == nil {
			return fmt.Errorf("no container builder found; install docker, podman, or buildah")
		}
	}

	// Compute image tag
	imageTag := computeImageTag(cfg.AgentID, cfg.Version, reg)

	// Build args
	buildArgs := map[string]string{}
	if devMode {
		buildArgs["FORGE_DEV"] = "true"
	}
	if cfg.Egress.Profile != "" {
		buildArgs["EGRESS_PROFILE"] = cfg.Egress.Profile
	}
	if cfg.Egress.Mode != "" {
		buildArgs["EGRESS_MODE"] = cfg.Egress.Mode
	}

	// Build container image
	fmt.Printf("Building image %s using %s...\n", imageTag, builder.Name())

	result, err := builder.Build(context.Background(), container.BuildOptions{
		ContextDir: outDir,
		Dockerfile: filepath.Join(outDir, "Dockerfile"),
		Tag:        imageTag,
		Platform:   platform,
		NoCache:    noCache,
		BuildArgs:  buildArgs,
	})
	if err != nil {
		return fmt.Errorf("container build failed: %w", err)
	}

	fmt.Printf("Image built: %s (ID: %s)\n", result.Tag, result.ImageID)

	// Optionally push
	pushed := false
	if pushImage {
		fmt.Printf("Pushing %s...\n", imageTag)
		if err := builder.Push(context.Background(), imageTag); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
		pushed = true
		fmt.Printf("Pushed: %s\n", imageTag)
	}

	// Write image manifest
	manifest := &container.ImageManifest{
		AgentID:  cfg.AgentID,
		Version:  cfg.Version,
		ImageTag: imageTag,
		Builder:  builder.Name(),
		Platform: platform,
		BuiltAt:  time.Now().UTC().Format(time.RFC3339),
		BuildDir: outDir,
		Pushed:   pushed,

		ForgeVersion:         "1.0",
		ToolInterfaceVersion: "1.0",
		EgressProfile:        cfg.Egress.Profile,
		EgressMode:           cfg.Egress.Mode,
		DevBuild:             devMode,
	}

	manifestPath := filepath.Join(outDir, "image-manifest.json")
	if err := container.WriteManifest(manifestPath, manifest); err != nil {
		return fmt.Errorf("writing image manifest: %w", err)
	}

	// Generate docker-compose.yaml if --with-channels is set
	if withChannels && len(cfg.Channels) > 0 {
		composePath := filepath.Join(outDir, "docker-compose.yaml")
		if err := generateDockerCompose(composePath, imageTag, cfg, 8080); err != nil {
			return fmt.Errorf("generating docker-compose.yaml: %w", err)
		}
		fmt.Printf("Generated %s\n", composePath)
	}

	// Verify container if requested
	if verifyFlag {
		fmt.Println("Verifying container...")
		if err := container.Verify(context.Background(), imageTag); err != nil {
			return fmt.Errorf("container verification failed: %w", err)
		}
		fmt.Println("Container verification passed.")
	}

	fmt.Println("Package complete.")
	return nil
}

// validateProdConfig checks that the config is valid for production builds.
func validateProdConfig(cfg *types.ForgeConfig) error {
	var toolNames []string
	for _, t := range cfg.Tools {
		toolNames = append(toolNames, t.Name)
	}
	v := export.ValidateProdConfig(cfg.Egress.Mode, toolNames)
	if len(v.Errors) > 0 {
		return fmt.Errorf("%s", v.Errors[0])
	}
	return nil
}

// channelComposeData holds data for a channel adapter in docker-compose.
type channelComposeData struct {
	Name    string
	EnvVars []string
}

// composeData holds template data for docker-compose generation.
type composeData struct {
	ImageTag      string
	Port          int
	ModelProvider string
	ModelName     string
	EgressProfile string
	EgressMode    string
	Channels      []channelComposeData
}

func generateDockerCompose(path string, imageTag string, cfg *types.ForgeConfig, port int) error {
	if port == 0 {
		port = 8080
	}

	tmplContent, err := templates.FS.ReadFile("docker-compose.yaml.tmpl")
	if err != nil {
		return fmt.Errorf("reading docker-compose template: %w", err)
	}

	tmpl, err := template.New("docker-compose").Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("parsing docker-compose template: %w", err)
	}

	var channels []channelComposeData
	for _, ch := range cfg.Channels {
		if ch != "slack" && ch != "telegram" {
			continue
		}
		cd := channelComposeData{Name: ch}
		switch ch {
		case "slack":
			cd.EnvVars = []string{
				"SLACK_SIGNING_SECRET=${SLACK_SIGNING_SECRET}",
				"SLACK_BOT_TOKEN=${SLACK_BOT_TOKEN}",
			}
		case "telegram":
			cd.EnvVars = []string{
				"TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}",
			}
		}
		channels = append(channels, cd)
	}

	data := composeData{
		ImageTag:      imageTag,
		Port:          port,
		ModelProvider: cfg.Model.Provider,
		ModelName:     cfg.Model.Name,
		EgressProfile: cfg.Egress.Profile,
		EgressMode:    cfg.Egress.Mode,
		Channels:      channels,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("executing docker-compose template: %w", err)
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

// ensureBuildOutput runs forge build if output is missing or stale.
func ensureBuildOutput(outDir, cfgPath string) error {
	manifestPath := filepath.Join(outDir, "build-manifest.json")
	needsBuild := false

	info, err := os.Stat(manifestPath)
	if os.IsNotExist(err) {
		needsBuild = true
	} else if err != nil {
		return fmt.Errorf("checking build manifest: %w", err)
	} else {
		// Check if forge.yaml is newer than build manifest
		cfgInfo, err := os.Stat(cfgPath)
		if err != nil {
			return fmt.Errorf("checking config file: %w", err)
		}
		if cfgInfo.ModTime().After(info.ModTime()) {
			needsBuild = true
		}
	}

	if needsBuild {
		fmt.Println("Running forge build...")
		if err := runBuild(nil, nil); err != nil {
			return fmt.Errorf("build step failed: %w", err)
		}
	}

	return nil
}

// computeImageTag constructs the image tag from agent ID, version, and optional registry.
func computeImageTag(agentID, version, reg string) string {
	if reg != "" {
		return fmt.Sprintf("%s/%s:%s", reg, agentID, version)
	}
	return fmt.Sprintf("%s:%s", agentID, version)
}
