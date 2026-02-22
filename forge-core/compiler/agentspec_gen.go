// Package compiler provides pure functions for generating and transforming AgentSpec data.
package compiler

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/types"
)

// ConfigToAgentSpec converts a ForgeConfig into an AgentSpec.
func ConfigToAgentSpec(cfg *types.ForgeConfig) *agentspec.AgentSpec {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      cfg.AgentID,
		Version:      cfg.Version,
		Name:         cfg.AgentID,
	}

	fields := strings.Fields(cfg.Entrypoint)
	spec.Runtime = &agentspec.RuntimeConfig{
		Image:      InferBaseImage(fields),
		Entrypoint: fields,
		Port:       8080,
	}

	for _, t := range cfg.Tools {
		ts := agentspec.ToolSpec{Name: t.Name}
		if t.Name == "cli_execute" && t.Config != nil {
			meta := &agentspec.ForgeToolMeta{}
			if bins, ok := t.Config["allowed_binaries"]; ok {
				if binSlice, ok := bins.([]any); ok {
					for _, b := range binSlice {
						if s, ok := b.(string); ok {
							meta.AllowedBinaries = append(meta.AllowedBinaries, s)
						}
					}
				}
			}
			if envPass, ok := t.Config["env_passthrough"]; ok {
				if envSlice, ok := envPass.([]any); ok {
					for _, e := range envSlice {
						if s, ok := e.(string); ok {
							meta.EnvPassthrough = append(meta.EnvPassthrough, s)
						}
					}
				}
			}
			ts.ForgeMeta = meta
		}
		spec.Tools = append(spec.Tools, ts)
	}

	if cfg.Model.Provider != "" || cfg.Model.Name != "" {
		spec.Model = &agentspec.ModelConfig{
			Provider: cfg.Model.Provider,
			Name:     cfg.Model.Name,
			Version:  cfg.Model.Version,
		}
	}

	if cfg.Egress.Profile != "" {
		spec.EgressProfile = cfg.Egress.Profile
	}
	if cfg.Egress.Mode != "" {
		spec.EgressMode = cfg.Egress.Mode
	}

	return spec
}

// InferBaseImage returns a container base image based on the entrypoint command.
func InferBaseImage(entrypoint []string) string {
	if len(entrypoint) == 0 {
		return "ubuntu:latest"
	}
	switch {
	case strings.HasPrefix(entrypoint[0], "python"):
		return "python:3.12-slim"
	case entrypoint[0] == "bun":
		return "oven/bun:latest"
	case entrypoint[0] == "go" || entrypoint[0] == "./main":
		return "golang:1.23-alpine"
	case entrypoint[0] == "node":
		return "node:20-slim"
	default:
		return "ubuntu:latest"
	}
}

// MergePluginConfig fills gaps in the spec with plugin-extracted values.
// forge.yaml values always take precedence.
func MergePluginConfig(spec *agentspec.AgentSpec, pc *plugins.AgentConfig) {
	// Name: only fill if still equal to AgentID (i.e., not explicitly set)
	if pc.Name != "" && spec.Name == spec.AgentID {
		spec.Name = pc.Name
	}

	// Description: only fill if empty
	if pc.Description != "" && spec.Description == "" {
		spec.Description = pc.Description
	}

	// Tools: append new tools or enrich existing ones by name
	existingTools := make(map[string]int, len(spec.Tools))
	for i, t := range spec.Tools {
		existingTools[t.Name] = i
	}
	for _, pt := range pc.Tools {
		if idx, ok := existingTools[pt.Name]; ok {
			// Enrich existing tool
			if spec.Tools[idx].Description == "" && pt.Description != "" {
				spec.Tools[idx].Description = pt.Description
			}
			if spec.Tools[idx].InputSchema == nil && pt.InputSchema != nil {
				data, err := json.Marshal(pt.InputSchema)
				if err == nil {
					spec.Tools[idx].InputSchema = data
				}
			}
		} else {
			// Append new tool
			tool := agentspec.ToolSpec{
				Name:        pt.Name,
				Description: pt.Description,
			}
			if pt.InputSchema != nil {
				data, err := json.Marshal(pt.InputSchema)
				if err == nil {
					tool.InputSchema = data
				}
			}
			spec.Tools = append(spec.Tools, tool)
		}
	}

	// Model: only fill if not already set from forge.yaml
	if pc.Model != nil && spec.Model == nil {
		spec.Model = &agentspec.ModelConfig{
			Provider: pc.Model.Provider,
			Name:     pc.Model.Name,
			Version:  pc.Model.Version,
		}
	}
}

// WrapperEntrypoint returns the entrypoint command for a generated wrapper file.
func WrapperEntrypoint(file string) []string {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".py":
		return []string{"python", file}
	case ".ts":
		return []string{"bun", "run", file}
	case ".go":
		return []string{"go", "run", file}
	default:
		return []string{"python", file}
	}
}
