// Package validate provides JSON Schema validation for Forge specifications.
package validate

import (
	"fmt"
	"sync"

	"github.com/initializ/forge/forge-core/schemas"
	"github.com/xeipuuv/gojsonschema"
)

var (
	compiledSchema *gojsonschema.Schema
	compileOnce    sync.Once
	compileErr     error
)

func getSchema() (*gojsonschema.Schema, error) {
	compileOnce.Do(func() {
		loader := gojsonschema.NewBytesLoader(schemas.AgentSpecV1Schema)
		compiledSchema, compileErr = gojsonschema.NewSchema(loader)
	})
	return compiledSchema, compileErr
}

// ValidateAgentSpec validates raw JSON bytes against the AgentSpec v1.0 schema.
// It returns a slice of validation error descriptions and an error if schema
// compilation fails.
func ValidateAgentSpec(jsonData []byte) ([]string, error) {
	schema, err := getSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling agent spec schema: %w", err)
	}

	result, err := schema.Validate(gojsonschema.NewBytesLoader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("validating agent spec: %w", err)
	}

	if result.Valid() {
		return nil, nil
	}

	errs := make([]string, 0, len(result.Errors()))
	for _, e := range result.Errors() {
		errs = append(errs, e.String())
	}
	return errs, nil
}
