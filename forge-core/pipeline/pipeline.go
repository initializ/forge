// Package pipeline provides a sequential stage-based execution pipeline.
package pipeline

import (
	"context"
	"fmt"
)

// Stage is a single unit of work in a build pipeline.
type Stage interface {
	Name() string
	Execute(ctx context.Context, bc *BuildContext) error
}

// PipelineOptions carries shared configuration for all pipeline stages.
type PipelineOptions struct {
	WorkDir    string
	OutputDir  string
	ConfigPath string
	Env        map[string]string
}

// Pipeline executes a sequence of stages in order.
type Pipeline struct {
	stages []Stage
}

// New creates a Pipeline from the given stages.
func New(stages ...Stage) *Pipeline {
	return &Pipeline{stages: stages}
}

// Run executes each stage sequentially. It stops on the first error.
func (p *Pipeline) Run(ctx context.Context, bc *BuildContext) error {
	for _, s := range p.stages {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("pipeline cancelled before stage %s: %w", s.Name(), err)
		}
		if err := s.Execute(ctx, bc); err != nil {
			return fmt.Errorf("stage %s: %w", s.Name(), err)
		}
	}
	return nil
}
