package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func twoItemSelect() MultiSelect {
	return MultiSelect{Items: []MultiSelectItem{
		{Label: "alpha", Value: "alpha"},
		{Label: "beta", Value: "beta"},
	}}
}

// TestMultiSelect_EnterConfirmsEmpty pins the fix: pressing Enter with
// nothing toggled confirms an EMPTY selection instead of silently
// auto-selecting the highlighted row.
func TestMultiSelect_EnterConfirmsEmpty(t *testing.T) {
	m := twoItemSelect()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Done() {
		t.Fatal("Enter should confirm the selection")
	}
	if vals := m.SelectedValues(); len(vals) != 0 {
		t.Errorf("Enter with nothing toggled must select nothing, got %v", vals)
	}
}

// TestMultiSelect_SpaceThenEnterSelects confirms Space still toggles the
// cursor row and Enter confirms it.
func TestMultiSelect_SpaceThenEnterSelects(t *testing.T) {
	m := twoItemSelect()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace}) // toggle cursor row (alpha)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	vals := m.SelectedValues()
	if len(vals) != 1 || vals[0] != "alpha" {
		t.Errorf("Space+Enter should select 'alpha', got %v", vals)
	}
}

// TestMultiSelect_MoveThenSpaceSelectsCorrectRow guards that toggling
// follows the cursor, not row 0.
func TestMultiSelect_MoveThenSpaceSelectsCorrectRow(t *testing.T) {
	m := twoItemSelect()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})  // cursor -> beta
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace}) // toggle beta
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	vals := m.SelectedValues()
	if len(vals) != 1 || vals[0] != "beta" {
		t.Errorf("expected only 'beta', got %v", vals)
	}
}
