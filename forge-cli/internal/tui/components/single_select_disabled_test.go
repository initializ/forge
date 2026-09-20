package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newDisabledSelect(items []SingleSelectItem) SingleSelect {
	z := lipgloss.Color("")
	return NewSingleSelect(items, z, z, z, z, z, z, z, lipgloss.NewStyle(), lipgloss.NewStyle())
}

func sendKey(s SingleSelect, k string) SingleSelect {
	s2, _ := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return s2
}

func TestDisabledItemInitialCursorSkips(t *testing.T) {
	s := newDisabledSelect([]SingleSelectItem{
		{Label: "On", Value: "on", Disabled: true},
		{Label: "Off", Value: "off"},
	})
	// Initial cursor must land on the first selectable item (Off), not the disabled On.
	if s.cursor != 1 {
		t.Fatalf("initial cursor = %d, want 1 (Off)", s.cursor)
	}
}

func TestDisabledItemUnselectable(t *testing.T) {
	s := newDisabledSelect([]SingleSelectItem{
		{Label: "On", Value: "on", Disabled: true},
		{Label: "Off", Value: "off"},
	})
	// Up should not move onto the disabled item.
	s = sendKey(s, "k") // up
	if s.cursor != 1 {
		t.Errorf("up onto disabled item: cursor=%d, want 1", s.cursor)
	}
	// Enter selects Off (the only selectable).
	s2, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !s2.Done() {
		t.Fatal("enter should select the enabled item")
	}
	if _, v := s2.Selected(); v != "off" {
		t.Errorf("selected=%q, want off", v)
	}
}

func TestSelectByValueSkipsDisabled(t *testing.T) {
	s := newDisabledSelect([]SingleSelectItem{
		{Label: "On", Value: "on", Disabled: true},
		{Label: "Off", Value: "off"},
	})
	s.SelectByValue("on") // disabled → must not move the cursor there
	if s.cursor == 0 {
		t.Error("SelectByValue moved cursor onto a disabled item")
	}
}

func TestEnabledItemsUnaffected(t *testing.T) {
	s := newDisabledSelect([]SingleSelectItem{
		{Label: "A", Value: "a"},
		{Label: "B", Value: "b"},
	})
	if s.cursor != 0 {
		t.Fatalf("cursor=%d, want 0", s.cursor)
	}
	s = sendKey(s, "j") // down
	if s.cursor != 1 {
		t.Errorf("down: cursor=%d, want 1", s.cursor)
	}
}
