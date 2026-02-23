package components

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FileStatus represents the generation status of a file.
type FileStatus int

const (
	FilePending FileStatus = iota
	FileWriting
	FileDone
	FileError
)

// FileEntry represents a file being generated.
type FileEntry struct {
	Icon   string
	Path   string
	Status FileStatus
}

// FileList shows progressive file generation with status icons.
type FileList struct {
	Files    []FileEntry
	revealed int // number of files revealed so far
	spinner  spinner.Model
	done     bool

	// Styles
	PrimaryStyle lipgloss.Style
	SuccessStyle lipgloss.Style
	ErrorStyle   lipgloss.Style
	DimStyle     lipgloss.Style
}

// NewFileList creates a new file list display.
func NewFileList(files []FileEntry, primaryStyle, successStyle, errorStyle, dimStyle lipgloss.Style, accentColor lipgloss.Color) FileList {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accentColor)

	return FileList{
		Files:        files,
		spinner:      sp,
		PrimaryStyle: primaryStyle,
		SuccessStyle: successStyle,
		ErrorStyle:   errorStyle,
		DimStyle:     dimStyle,
	}
}

// Init starts the spinner and file reveal timer.
func (f FileList) Init() tea.Cmd {
	return tea.Batch(f.spinner.Tick, f.tickCmd())
}

// Update handles messages.
func (f FileList) Update(msg tea.Msg) (FileList, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		f.spinner, cmd = f.spinner.Update(msg)
		return f, cmd
	case fileRevealMsg:
		if f.revealed < len(f.Files) {
			f.Files[f.revealed].Status = FileDone
			f.revealed++
			if f.revealed < len(f.Files) {
				return f, f.tickCmd()
			}
			f.done = true
		}
		return f, nil
	}
	return f, nil
}

// View renders the file list.
func (f FileList) View(width int) string {
	var out string

	for i, file := range f.Files {
		if i >= f.revealed && i > 0 && f.Files[i-1].Status != FileDone {
			break
		}

		var statusIcon string
		var pathStyle lipgloss.Style

		switch file.Status {
		case FilePending:
			if i == f.revealed {
				statusIcon = f.spinner.View()
				pathStyle = f.PrimaryStyle
			} else {
				statusIcon = f.DimStyle.Render("·")
				pathStyle = f.DimStyle
			}
		case FileWriting:
			statusIcon = f.spinner.View()
			pathStyle = f.PrimaryStyle
		case FileDone:
			statusIcon = f.SuccessStyle.Render("✓")
			pathStyle = f.SuccessStyle
		case FileError:
			statusIcon = f.ErrorStyle.Render("✗")
			pathStyle = f.ErrorStyle
		}

		icon := file.Icon
		if icon == "" {
			icon = "📄"
		}

		out += fmt.Sprintf("    %s %s %s\n", statusIcon, icon, pathStyle.Render(file.Path))
	}

	return out
}

// Done returns true when all files have been revealed.
func (f FileList) Done() bool {
	return f.done
}

// MarkFile updates a specific file's status.
func (f *FileList) MarkFile(index int, status FileStatus) {
	if index >= 0 && index < len(f.Files) {
		f.Files[index].Status = status
	}
}

// RevealAll marks all files as done immediately.
func (f *FileList) RevealAll() {
	for i := range f.Files {
		f.Files[i].Status = FileDone
	}
	f.revealed = len(f.Files)
	f.done = true
}

type fileRevealMsg struct{}

func (f FileList) tickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return fileRevealMsg{}
	})
}
