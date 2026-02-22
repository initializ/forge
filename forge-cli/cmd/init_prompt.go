package cmd

import (
	"fmt"
	"strings"

	"github.com/manifoldco/promptui"
)

// askText prompts the user for a text value with an optional default.
func askText(label, defaultVal string) (string, error) {
	p := promptui.Prompt{
		Label:   label,
		Default: defaultVal,
	}
	result, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("prompt %q failed: %w", label, err)
	}
	return result, nil
}

// askSelect presents a list of items and returns the selected index and value.
func askSelect(label string, items []string) (int, string, error) {
	s := promptui.Select{
		Label: label,
		Items: items,
	}
	idx, val, err := s.Run()
	if err != nil {
		return -1, "", fmt.Errorf("prompt %q failed: %w", label, err)
	}
	return idx, val, nil
}

// askMultiSelect lets the user confirm each item individually, returning selected items.
func askMultiSelect(label string, items []string) ([]string, error) {
	fmt.Printf("%s (confirm each):\n", label)
	var selected []string
	for _, item := range items {
		p := promptui.Prompt{
			Label:     fmt.Sprintf("  Include %s", item),
			IsConfirm: true,
		}
		if _, err := p.Run(); err == nil {
			selected = append(selected, item)
		}
	}
	return selected, nil
}

// askConfirm asks for a yes/no confirmation.
func askConfirm(label string) (bool, error) {
	p := promptui.Prompt{
		Label:     label,
		IsConfirm: true,
	}
	_, err := p.Run()
	if err != nil {
		// promptui returns an error for "No" — distinguish from real errors
		if strings.Contains(err.Error(), "^C") || err == promptui.ErrAbort {
			return false, fmt.Errorf("prompt aborted")
		}
		return false, nil
	}
	return true, nil
}

// askPassword prompts for a secret value with character masking.
func askPassword(label string) (string, error) {
	p := promptui.Prompt{
		Label: label,
		Mask:  '*',
	}
	result, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("prompt %q failed: %w", label, err)
	}
	return result, nil
}
