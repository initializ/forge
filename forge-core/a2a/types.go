// Package a2a provides shared types for the Agent-to-Agent (A2A) protocol.
package a2a

// TaskState represents the possible states of an A2A task.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateAuthRequired  TaskState = "auth-required"
	TaskStateRejected      TaskState = "rejected"
)

// TaskStatus holds the current state of a task along with an optional message.
type TaskStatus struct {
	State   TaskState `json:"state"`
	Message *Message  `json:"message,omitempty"`
}

// Task represents an A2A task exchanged between agents.
type Task struct {
	ID        string         `json:"id"`
	Status    TaskStatus     `json:"status"`
	History   []Message      `json:"history,omitempty"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// MessageRole indicates who produced a message.
type MessageRole string

const (
	MessageRoleUser  MessageRole = "user"
	MessageRoleAgent MessageRole = "agent"
)

// Message is a single conversational turn in the A2A protocol.
type Message struct {
	Role  MessageRole `json:"role"`
	Parts []Part      `json:"parts"`
}

// PartKind discriminates the content type of a Part.
type PartKind string

const (
	PartKindText PartKind = "text"
	PartKindData PartKind = "data"
	PartKindFile PartKind = "file"
)

// Part is a flat union struct representing a piece of message content.
// Exactly one of Text, Data, or File should be set, indicated by Kind.
type Part struct {
	Kind PartKind     `json:"kind"`
	Text string       `json:"text,omitempty"`
	Data any          `json:"data,omitempty"`
	File *FileContent `json:"file,omitempty"`
}

// FileContent holds the contents or reference for a file part.
type FileContent struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Bytes    []byte `json:"bytes,omitempty"`
}

// Artifact is a named output produced by an agent task.
type Artifact struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts"`
}

// AgentCard describes an agent's capabilities for discovery.
type AgentCard struct {
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	URL          string             `json:"url"`
	Skills       []Skill            `json:"skills,omitempty"`
	Capabilities *AgentCapabilities `json:"capabilities,omitempty"`
}

// AgentCapabilities declares optional A2A features an agent supports.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

// Skill describes a discrete capability an agent exposes.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// NewTextPart creates a Part containing text content.
func NewTextPart(text string) Part {
	return Part{Kind: PartKindText, Text: text}
}

// NewDataPart creates a Part containing structured data.
func NewDataPart(data any) Part {
	return Part{Kind: PartKindData, Data: data}
}

// NewFilePart creates a Part referencing a file.
func NewFilePart(file FileContent) Part {
	return Part{Kind: PartKindFile, File: &file}
}
