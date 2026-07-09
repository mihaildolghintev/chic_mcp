package llm

import "encoding/json"

// Message is one entry of an OpenAI-style chat. Content is either plain text
// (Text) or multimodal parts (Parts, which win when non-empty) — the split
// exists because the wire format is a string for the common case and an array
// of typed parts for vision requests.
type Message struct {
	Role       string
	Text       string
	Parts      []ContentPart
	ToolCalls  []ToolCall
	ToolCallID string
}

// ContentPart is one element of a multimodal message.
type ContentPart struct {
	Type     string    `json:"type"` // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL carries an image as an https URL or a base64 data URI.
type ImageURL struct {
	URL string `json:"url"`
}

// ToolCall is the model's request to invoke one function.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall names the function and carries its arguments as a JSON string
// (the OpenAI wire format — not an object).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function definition advertised to the model.
type Tool struct {
	Type     string   `json:"type"` // always "function"
	Function Function `json:"function"`
}

// Function describes one callable function; Parameters is a JSON Schema object.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Usage is the token accounting a provider reports per completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// System, User, Assistant and ToolResult build the common plain-text messages.
func System(text string) Message    { return Message{Role: "system", Text: text} }
func User(text string) Message      { return Message{Role: "user", Text: text} }
func Assistant(text string) Message { return Message{Role: "assistant", Text: text} }

// ToolResult wraps a tool's output as the "tool" role message the model
// expects after emitting a tool call with this id.
func ToolResult(toolCallID, text string) Message {
	return Message{Role: "tool", ToolCallID: toolCallID, Text: text}
}

// UserImage builds a vision message: an image (as a data URI or URL) with an
// optional text caption acting as the prompt.
func UserImage(caption, imageURL string) Message {
	parts := []ContentPart{}
	if caption != "" {
		parts = append(parts, ContentPart{Type: "text", Text: caption})
	}
	parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: imageURL}})
	return Message{Role: "user", Parts: parts}
}

// HasImage reports whether the message carries an image part — the signal the
// router uses to pick a vision-capable provider.
func (m Message) HasImage() bool {
	for _, p := range m.Parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

// wireMessage is the JSON shape; Content is string or []ContentPart.
type wireMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// MarshalJSON emits Parts as a content array when present, Text otherwise.
func (m Message) MarshalJSON() ([]byte, error) {
	w := wireMessage{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	if len(m.Parts) > 0 {
		w.Content = m.Parts
	} else {
		w.Content = m.Text
	}
	return json.Marshal(w)
}

// UnmarshalJSON accepts content as a string, null (assistant tool-call turns)
// or a parts array (round-tripping our own requests).
func (m *Message) UnmarshalJSON(data []byte) error {
	var w struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	m.Role, m.ToolCalls, m.ToolCallID = w.Role, w.ToolCalls, w.ToolCallID
	m.Text, m.Parts = "", nil
	if len(w.Content) == 0 || string(w.Content) == "null" {
		return nil
	}
	if w.Content[0] == '[' {
		return json.Unmarshal(w.Content, &m.Parts)
	}
	return json.Unmarshal(w.Content, &m.Text)
}
