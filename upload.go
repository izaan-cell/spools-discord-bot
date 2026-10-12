package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Mirrors api/_lib/mediarules.Rules/ContentTypes in the main Spools repo
// (not imported - this module is deliberately standalone, see main.go's
// package comment). Keep in sync by hand if those ever change. Purely a
// client-side pre-check for a nicer error message - api/_lib/uploadcore on
// the server is what actually enforces this, a stale copy here can never
// let something invalid through.
var extensionCategory = map[string]string{
	"jpg": "image", "jpeg": "image", "png": "image", "webp": "image",
	"gif": "gif",
	"mp4": "video", "mov": "video",
}

var extensionContentType = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp",
	"gif": "image/gif",
	"mp4": "video/mp4", "mov": "video/quicktime",
}

func categoryFor(filename string) (category, ext string, ok bool) {
	ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	category, ok = extensionCategory[ext]
	return category, ext, ok
}

// uploadResult is one attachment's outcome, used to build the result embed
// after a button click or /upload - Link is set on success, Err on failure.
type uploadResult struct {
	Filename string
	Link     string
	Err      error
}

// uploadAttachment downloads the attachment from Discord and pushes it
// through api/discord-bot's upload-presign -> PUT -> upload-confirm, the
// same three steps api/upload's own presign/confirm do for the website -
// just fronted by one more hop so the bot never needs a Spools API token at
// all, only the shared bot secret already on every request (see botAPI).
// Server-side resolves discordUserID to whichever Spools account should own
// this (their own linked account, or the shared fallback); discordUsername
// is only stored (as display metadata, for the fallback case) when the
// server decides it's actually needed, never assumed here.
func uploadAttachment(cfg config, discordUserID, discordUsername string, att *discordgo.MessageAttachment, customName, description string) uploadResult {
	category, ext, ok := categoryFor(att.Filename)
	if !ok {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("unsupported file type")}
	}

	contentType := extensionContentType[ext]
	if contentType == "" {
		contentType = att.ContentType
	}

	// Keep the real extension regardless of what's typed in the name field -
	// the content-type/category checks above already depend on it, and a
	// renamed-without-an-extension file would otherwise stop being
	// recognized as an image/gif/video everywhere else that reads it back.
	filename := att.Filename
	if customName != "" {
		filename = strings.TrimSuffix(customName, "."+ext) + "." + ext
	}

	data, err := downloadAttachment(att.URL)
	if err != nil {
		return uploadResult{Filename: filename, Err: fmt.Errorf("download failed: %w", err)}
	}

	var presigned struct {
		UploadURL string `json:"uploadUrl"`
		Key       string `json:"key"`
	}
	err = botAPI(cfg, "upload-presign", map[string]any{
		"discordUserId": discordUserID,
		"filename":      filename,
		"contentType":   contentType,
		"size":          len(data),
		"category":      category,
	}, &presigned)
	if err != nil {
		return uploadResult{Filename: filename, Err: fmt.Errorf("presign failed: %w", err)}
	}

	if err := putObject(presigned.UploadURL, contentType, data); err != nil {
		return uploadResult{Filename: filename, Err: fmt.Errorf("upload to storage failed: %w", err)}
	}

	var confirmed struct {
		Key string `json:"key"`
		URL string `json:"url"`
	}
	err = botAPI(cfg, "upload-confirm", map[string]any{
		"discordUserId":   discordUserID,
		"discordUsername": discordUsername,
		"key":             presigned.Key,
		"filename":        filename,
		"contentType":     contentType,
		"size":            len(data),
		"category":        category,
		"isPublic":        true,
		"description":     description,
	}, &confirmed)
	if err != nil {
		return uploadResult{Filename: filename, Err: fmt.Errorf("confirm failed: %w", err)}
	}

	return uploadResult{Filename: filename, Link: cfg.apiBase + confirmed.URL}
}

func downloadAttachment(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord returned %d fetching attachment", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

type apiErrorBody struct {
	Error string `json:"error"`
}

// botAPI calls POST {apiBase}/api/discord-bot, authenticated with the one
// shared secret this bot has (DISCORD_BOT_SECRET) - never a Spools API
// token for any account. Every action (link, unlink, prefs, uploads, edit)
// goes through this one function.
func botAPI(cfg config, action string, payload map[string]any, out any) error {
	payload["action"] = action
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.apiBase+"/api/discord-bot", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.botSecret)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorBody
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("%s", apiErr.Error)
		}
		return fmt.Errorf("spools api returned %d", resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func putObject(uploadURL, contentType string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("storage returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
