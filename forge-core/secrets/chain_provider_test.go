package secrets

import (
	"fmt"
	"testing"
)

// stubProvider is a test helper that returns preconfigured values.
type stubProvider struct {
	name   string
	values map[string]string
	err    error // if set, all calls to Get return this error
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Get(key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	v, ok := s.values[key]
	if !ok {
		return "", &ErrSecretNotFound{Key: key, Provider: s.name}
	}
	return v, nil
}
func (s *stubProvider) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	keys := make([]string, 0, len(s.values))
	for k := range s.values {
		keys = append(keys, k)
	}
	return keys, nil
}

func TestChainProvider_PriorityOrder(t *testing.T) {
	p1 := &stubProvider{name: "first", values: map[string]string{"KEY": "from-first"}}
	p2 := &stubProvider{name: "second", values: map[string]string{"KEY": "from-second"}}

	chain := NewChainProvider(p1, p2)

	val, err := chain.Get("KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "from-first" {
		t.Fatalf("expected 'from-first', got %q", val)
	}
}

func TestChainProvider_Fallthrough(t *testing.T) {
	p1 := &stubProvider{name: "first", values: map[string]string{}}
	p2 := &stubProvider{name: "second", values: map[string]string{"KEY": "from-second"}}

	chain := NewChainProvider(p1, p2)

	val, err := chain.Get("KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "from-second" {
		t.Fatalf("expected 'from-second', got %q", val)
	}
}

func TestChainProvider_NotFound(t *testing.T) {
	p1 := &stubProvider{name: "first", values: map[string]string{}}
	p2 := &stubProvider{name: "second", values: map[string]string{}}

	chain := NewChainProvider(p1, p2)

	_, err := chain.Get("MISSING")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected ErrSecretNotFound, got %T", err)
	}
}

func TestChainProvider_ErrorPropagation(t *testing.T) {
	realErr := fmt.Errorf("decrypt failed")
	p1 := &stubProvider{name: "broken", err: realErr}
	p2 := &stubProvider{name: "second", values: map[string]string{"KEY": "ok"}}

	chain := NewChainProvider(p1, p2)

	_, err := chain.Get("KEY")
	if err == nil {
		t.Fatal("expected error propagation")
	}
	if err != realErr {
		t.Fatalf("expected original error, got %v", err)
	}
}

func TestChainProvider_UnionList(t *testing.T) {
	p1 := &stubProvider{name: "first", values: map[string]string{"A": "1", "B": "2"}}
	p2 := &stubProvider{name: "second", values: map[string]string{"B": "3", "C": "4"}}

	chain := NewChainProvider(p1, p2)

	keys, err := chain.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	keySet := make(map[string]bool)
	for _, k := range keys {
		keySet[k] = true
	}

	if len(keySet) != 3 {
		t.Fatalf("expected 3 unique keys, got %d: %v", len(keySet), keys)
	}
	for _, expected := range []string{"A", "B", "C"} {
		if !keySet[expected] {
			t.Fatalf("expected key %q in list", expected)
		}
	}
}

func TestChainProvider_GetWithSource(t *testing.T) {
	p1 := &stubProvider{name: "first", values: map[string]string{}}
	p2 := &stubProvider{name: "second", values: map[string]string{"KEY": "val"}}

	chain := NewChainProvider(p1, p2)

	val, source, err := chain.GetWithSource("KEY")
	if err != nil {
		t.Fatalf("GetWithSource: %v", err)
	}
	if val != "val" {
		t.Fatalf("expected 'val', got %q", val)
	}
	if source != "second" {
		t.Fatalf("expected source 'second', got %q", source)
	}
}

func TestChainProvider_Name(t *testing.T) {
	chain := NewChainProvider()
	if chain.Name() != "chain" {
		t.Fatalf("expected 'chain', got %q", chain.Name())
	}
}
