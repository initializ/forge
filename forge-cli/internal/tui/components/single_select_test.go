package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newTestSelect(values ...string) SingleSelect {
	items := make([]SingleSelectItem, len(values))
	for i, v := range values {
		items[i] = SingleSelectItem{Label: v, Value: v}
	}
	var c lipgloss.Color
	var st lipgloss.Style
	return NewSingleSelect(items, c, c, c, c, c, c, c, st, st)
}

func TestSingleSelect_SelectByValue_PreselectsThenConfirms(t *testing.T) {
	s := newTestSelect("a", "b", "c")
	s.SelectByValue("b") // move the highlight to "b"

	// SelectByValue must NOT auto-confirm.
	if s.Done() {
		t.Fatal("SelectByValue must not mark the selection done")
	}

	// Pressing enter now confirms the preselected item.
	s2, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	idx, val := s2.Selected()
	if idx != 1 || val != "b" {
		t.Errorf("preselect+enter = (%d, %q), want (1, \"b\")", idx, val)
	}
}

func TestSingleSelect_SelectByValue_EmptyAndUnknownAreNoops(t *testing.T) {
	for _, v := range []string{"", "does-not-exist"} {
		s := newTestSelect("a", "b")
		s.SelectByValue(v)
		s2, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if _, val := s2.Selected(); val != "a" {
			t.Errorf("SelectByValue(%q) should leave the cursor at the first item; enter gave %q", v, val)
		}
	}
}
