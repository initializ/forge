package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestChannelAddSlack(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	// Create a minimal forge.yaml
	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
channels:
  - a2a
`)

	err := runChannelAdd(nil, []string{"slack"})
	if err != nil {
		t.Fatalf("runChannelAdd(slack) error: %v", err)
	}

	// Check slack-config.yaml was created
	cfgPath := filepath.Join(dir, "slack-config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("slack-config.yaml not created")
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading slack-config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "adapter: slack") {
		t.Error("slack-config.yaml missing adapter field")
	}

	// Check .env was updated
	envPath := filepath.Join(dir, ".env")
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	if !strings.Contains(string(envData), "SLACK_SIGNING_SECRET") {
		t.Error(".env missing SLACK_SIGNING_SECRET")
	}
	if !strings.Contains(string(envData), "SLACK_BOT_TOKEN") {
		t.Error(".env missing SLACK_BOT_TOKEN")
	}

	// Check forge.yaml was updated with slack channel
	forgeData, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(forgeData, &doc); err != nil {
		t.Fatalf("parsing forge.yaml: %v", err)
	}
	chList, ok := doc["channels"].([]any)
	if !ok {
		t.Fatal("channels not found in forge.yaml")
	}
	found := false
	for _, ch := range chList {
		if ch == "slack" {
			found = true
			break
		}
	}
	if !found {
		t.Error("slack not added to channels list")
	}
}

func TestChannelAddTelegram(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
`)

	err := runChannelAdd(nil, []string{"telegram"})
	if err != nil {
		t.Fatalf("runChannelAdd(telegram) error: %v", err)
	}

	// Check telegram-config.yaml was created
	cfgPath := filepath.Join(dir, "telegram-config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("telegram-config.yaml not created")
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading telegram-config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "adapter: telegram") {
		t.Error("telegram-config.yaml missing adapter field")
	}
	if !strings.Contains(string(data), "mode: polling") {
		t.Error("telegram-config.yaml missing polling mode default")
	}

	// Check .env
	envData, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	if !strings.Contains(string(envData), "TELEGRAM_BOT_TOKEN") {
		t.Error(".env missing TELEGRAM_BOT_TOKEN")
	}
}

func TestChannelAddUnsupported(t *testing.T) {
	err := runChannelAdd(nil, []string{"discord"})
	if err == nil {
		t.Fatal("expected error for unsupported adapter")
	}
}

func TestChannelAddIdempotent(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
channels:
  - slack
`)

	// Adding slack when it's already in the channels list should succeed
	err := runChannelAdd(nil, []string{"slack"})
	if err != nil {
		t.Fatalf("runChannelAdd(slack) second time error: %v", err)
	}

	// Verify slack appears only once
	forgeData, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	var doc map[string]any
	yaml.Unmarshal(forgeData, &doc) //nolint:errcheck
	chList := doc["channels"].([]any)
	count := 0
	for _, ch := range chList {
		if ch == "slack" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("slack appears %d times, want 1", count)
	}
}

func TestChannelServeNoAgentURL(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	// Write a valid channel config
	os.WriteFile(filepath.Join(dir, "slack-config.yaml"), []byte(`
adapter: slack
webhook_port: 3000
settings:
  signing_secret: test
  bot_token: test
`), 0644) //nolint:errcheck

	t.Setenv("AGENT_URL", "")

	err := runChannelServe(nil, []string{"slack"})
	if err == nil {
		t.Fatal("expected error when AGENT_URL is not set")
	}
}

func TestAddChannelToForgeYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge.yaml")

	os.WriteFile(path, []byte(`
agent_id: test
version: 0.1.0
entrypoint: run.py
`), 0644) //nolint:errcheck

	err := addChannelToForgeYAML(path, "slack")
	if err != nil {
		t.Fatalf("addChannelToForgeYAML() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(data, &doc) //nolint:errcheck

	chList, ok := doc["channels"].([]any)
	if !ok {
		t.Fatal("channels not found")
	}
	if len(chList) != 1 || chList[0] != "slack" {
		t.Errorf("channels = %v, want [slack]", chList)
	}
}

func TestChannelAddSlack_UpdatesEgress(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
`)

	err := runChannelAdd(nil, []string{"slack"})
	if err != nil {
		t.Fatalf("runChannelAdd(slack) error: %v", err)
	}

	// Check egress.capabilities contains "slack"
	forgeData, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(forgeData, &doc); err != nil {
		t.Fatalf("parsing forge.yaml: %v", err)
	}
	egressMap, ok := doc["egress"].(map[string]any)
	if !ok {
		t.Fatal("egress not found in forge.yaml")
	}
	caps, ok := egressMap["capabilities"].([]any)
	if !ok {
		t.Fatal("egress.capabilities not found")
	}
	found := false
	for _, c := range caps {
		if c == "slack" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("slack not in egress.capabilities: %v", caps)
	}
}

func TestChannelAddTelegram_UpdatesEgress(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
`)

	err := runChannelAdd(nil, []string{"telegram"})
	if err != nil {
		t.Fatalf("runChannelAdd(telegram) error: %v", err)
	}

	// Check egress.allowed_domains contains "api.telegram.org"
	forgeData, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(forgeData, &doc); err != nil {
		t.Fatalf("parsing forge.yaml: %v", err)
	}
	egressMap, ok := doc["egress"].(map[string]any)
	if !ok {
		t.Fatal("egress not found in forge.yaml")
	}
	domains, ok := egressMap["allowed_domains"].([]any)
	if !ok {
		t.Fatal("egress.allowed_domains not found")
	}
	found := false
	for _, d := range domains {
		if d == "api.telegram.org" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("api.telegram.org not in egress.allowed_domains: %v", domains)
	}
}

func TestAddChannelEgressIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge.yaml")

	os.WriteFile(path, []byte(`
agent_id: test
version: 0.1.0
entrypoint: run.py
egress:
  capabilities:
    - slack
  allowed_domains:
    - api.telegram.org
`), 0644) //nolint:errcheck

	// Adding slack again should not duplicate
	if err := addChannelEgressToForgeYAML(path, "slack"); err != nil {
		t.Fatalf("addChannelEgressToForgeYAML(slack) error: %v", err)
	}
	// Adding telegram again should not duplicate
	if err := addChannelEgressToForgeYAML(path, "telegram"); err != nil {
		t.Fatalf("addChannelEgressToForgeYAML(telegram) error: %v", err)
	}

	data, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(data, &doc) //nolint:errcheck
	egressMap := doc["egress"].(map[string]any)

	// Check slack appears only once
	caps := egressMap["capabilities"].([]any)
	count := 0
	for _, c := range caps {
		if c == "slack" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("slack appears %d times in capabilities, want 1", count)
	}

	// Check telegram domain appears only once
	domains := egressMap["allowed_domains"].([]any)
	count = 0
	for _, d := range domains {
		if d == "api.telegram.org" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("api.telegram.org appears %d times in allowed_domains, want 1", count)
	}
}

func TestGenerateChannelConfig(t *testing.T) {
	slack := generateChannelConfig("slack")
	if !strings.Contains(slack, "adapter: slack") {
		t.Error("slack config missing adapter")
	}

	tg := generateChannelConfig("telegram")
	if !strings.Contains(tg, "adapter: telegram") {
		t.Error("telegram config missing adapter")
	}

	unknown := generateChannelConfig("discord")
	if unknown != "" {
		t.Errorf("unknown adapter should return empty, got %q", unknown)
	}
}
