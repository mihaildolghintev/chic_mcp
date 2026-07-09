package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ErrPhotoTooLarge marks a photo over the download cap so the handler can
// answer with a specific message instead of a generic failure.
var ErrPhotoTooLarge = errors.New("photo exceeds size limit")

// maxPhotoBytes caps photo downloads. The Bot API itself refuses to serve
// files over 20 MB, so this is the natural ceiling.
const maxPhotoBytes = 20 << 20

// photoHTTP downloads photo bytes from api.telegram.org; separate from the
// library's client because file downloads are plain HTTP GETs.
var photoHTTP = &http.Client{Timeout: time.Minute}

// PhotoDataURI downloads the largest size of the message's photo and returns
// it as a base64 data URI ready for a vision model. Telegram re-encodes all
// photos as JPEG, so the MIME type is fixed.
func (b *Bot) PhotoDataURI(ctx context.Context, msg *models.Message) (string, error) {
	if len(msg.Photo) == 0 {
		return "", errors.New("message has no photo")
	}
	largest := msg.Photo[0]
	for _, p := range msg.Photo[1:] {
		if p.Width*p.Height > largest.Width*largest.Height {
			largest = p
		}
	}
	if largest.FileSize > maxPhotoBytes {
		return "", ErrPhotoTooLarge
	}

	f, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: largest.FileID})
	if err != nil {
		return "", fmt.Errorf("getFile: %w", err)
	}
	if f.FileSize > maxPhotoBytes {
		return "", ErrPhotoTooLarge
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api.FileDownloadLink(f), nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := photoHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download photo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download photo: status %d", resp.StatusCode)
	}

	// +1 so an oversized body is detectable rather than silently clipped.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return "", fmt.Errorf("read photo: %w", err)
	}
	if len(data) > maxPhotoBytes {
		return "", ErrPhotoTooLarge
	}

	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}
