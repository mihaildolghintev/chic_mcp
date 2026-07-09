package telegram

// Update is an incoming Telegram update. Only the fields the bot consumes are
// declared; the webhook subscribes to message updates only (see SetWebhook).
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is an incoming chat message.
type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
}

// User is the sender of a message.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is the conversation a message belongs to.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// PhotoSize is one resolution of a photo attachment. Telegram sends several
// sizes per photo; pick the largest for vision input.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// File is the getFile result: FilePath is joined with the file download root
// to fetch the bytes.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

// WebhookInfo is the getWebhookInfo result, used to verify registration.
type WebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int    `json:"pending_update_count"`
	LastErrorMessage   string `json:"last_error_message"`
}
