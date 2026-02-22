package tui

// StepBackMsg is emitted by a step when the user presses backspace at the first sub-phase.
type StepBackMsg struct{}

// StepCompleteMsg is emitted by a step when it finishes.
type StepCompleteMsg struct{}

// ValidationResultMsg carries the result of an async validation.
type ValidationResultMsg struct {
	Err error
}

// GenerationProgressMsg reports file generation progress.
type GenerationProgressMsg struct {
	File   string
	Status string // "writing", "done", "error"
}

// GenerationDoneMsg signals that all file generation is complete.
type GenerationDoneMsg struct {
	Err error
}

// FileTickMsg triggers the next file generation display.
type FileTickMsg struct{}
