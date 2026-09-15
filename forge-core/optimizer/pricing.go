package optimizer

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// ModelPrice is USD per 1,000,000 tokens for one model. Zero cache fields are
// derived from Input (read = 0.1×, write = 1.25×) — the standard Anthropic
// ratios — so a pricing file only needs input/output to be useful.
type ModelPrice struct {
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
}

// withDerivedCache fills zero cache fields from Input using the standard ratios.
func (m ModelPrice) withDerivedCache() ModelPrice {
	if m.CacheReadPerMTok == 0 {
		m.CacheReadPerMTok = m.InputPerMTok * 0.1
	}
	if m.CacheWritePerMTok == 0 {
		m.CacheWritePerMTok = m.InputPerMTok * 1.25
	}
	return m
}

// DefaultPrices are public list prices (USD per 1M tokens). Enterprises with
// negotiated rates override these via a pricing file — these are only the
// fallback so a fresh install shows real dollars out of the box. Keys are
// matched by longest prefix, so "claude-opus-4-8[1m]" resolves to the
// "claude-opus-4-8" entry.
var DefaultPrices = map[string]ModelPrice{
	"claude-fable-5":    {InputPerMTok: 10, OutputPerMTok: 50},
	"claude-opus-4-8":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-7":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-6":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-5":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-sonnet-5":   {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5},
}

// DefaultUnknownPrice prices models not in the table (including non-Anthropic
// models seen through a chained gateway). A mid-tier rate keeps the "unknown"
// bucket from being $0; override it in a pricing file.
var DefaultUnknownPrice = ModelPrice{InputPerMTok: 3, OutputPerMTok: 15}

// Pricing resolves a model id to a price, with an overridable table.
type Pricing struct {
	prices  map[string]ModelPrice
	keys    []string // sorted longest-first for prefix matching
	unknown ModelPrice
}

// NewPricing builds a Pricing from the defaults merged with overrides (override
// wins per model). Pass nil overrides for defaults only.
func NewPricing(overrides map[string]ModelPrice) *Pricing {
	merged := make(map[string]ModelPrice, len(DefaultPrices)+len(overrides))
	for k, v := range DefaultPrices {
		merged[k] = v.withDerivedCache()
	}
	for k, v := range overrides {
		merged[k] = v.withDerivedCache()
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	// Longest key first so the most specific prefix wins.
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	unknown := DefaultUnknownPrice
	if u, ok := overrides["unknown"]; ok {
		unknown = u
	}
	return &Pricing{prices: merged, keys: keys, unknown: unknown.withDerivedCache()}
}

// LoadPricingFile reads a JSON map of model → ModelPrice. The special key
// "unknown" sets the fallback price. Missing file is not an error (returns nil).
func LoadPricingFile(path string) (map[string]ModelPrice, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]ModelPrice
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// For returns the price for a model id (exact, then longest-prefix, then
// unknown).
func (p *Pricing) For(model string) ModelPrice {
	if model == "" {
		return p.unknown
	}
	if m, ok := p.prices[model]; ok {
		return m
	}
	for _, k := range p.keys {
		if strings.HasPrefix(model, k) {
			return p.prices[k]
		}
	}
	return p.unknown
}

// CostAvoided returns the USD saved by compressing savedTokens for a model.
// Compression shrinks the request's INPUT, so savings are valued at the input
// rate — a directional estimate (some saved tokens would have been billed as
// cheaper cache reads, some as full-price input).
func (p *Pricing) CostAvoided(model string, savedTokens int64) float64 {
	return float64(savedTokens) * p.For(model).InputPerMTok / 1_000_000
}
