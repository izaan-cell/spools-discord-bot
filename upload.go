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
// package comment). Keep in sync by hand if those ever change.
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

// categoryFor mirrors api/upload's own rule: the extension decides the
// category, anything unrecognized is skipped rather than guessed at.
func categoryFor(filename string) (category, ext string, ok bool) {
	ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	category, ok = extensionCategory[ext]
	return category, ext, ok
}

type presignRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Category    string `json:"category"`
}

type presignResponse struct {
	UploadURL string `json:"uploadUrl"`
	Key       string `json:"key"`
}

type confirmRequest struct {
	Key         string `json:"key"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Category    string `json:"category"`
}

type confirmResponse struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type apiErrorBody struct {
	Error string `json:"error"`
}

// uploadResult is one attachment's outcome, used to build the result embed
// after a button click - Link is set on success, Err on failure, never both.
type uploadResult struct {
	Filename string
	Link     string
	Err      error
}

func uploadAttachment(cfg config, att *discordgo.MessageAttachment) uploadResult {
	category, ext, ok := categoryFor(att.Filename)
	if !ok {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("unsupported file type")}
	}

	contentType := extensionContentType[ext]
	if contentType == "" {
		contentType = att.ContentType
	}

	data, err := downloadAttachment(att.URL)
	if err != nil {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("download failed: %w", err)}
	}

	var presigned presignResponse
	err = spoolsPost(cfg, "presign", presignRequest{
		Filename:    att.Filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Category:    category,
	}, &presigned)
	if err != nil {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("presign failed: %w", err)}
	}

	if err := putObject(presigned.UploadURL, contentType, data); err != nil {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("upload to storage failed: %w", err)}
	}

	var confirmed confirmResponse
	err = spoolsPost(cfg, "confirm", confirmRequest{
		Key:         presigned.Key,
		Filename:    att.Filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Category:    category,
	}, &confirmed)
	if err != nil {
		return uploadResult{Filename: att.Filename, Err: fmt.Errorf("confirm failed: %w", err)}
	}

	return uploadResult{Filename: att.Filename, Link: cfg.apiBase + confirmed.URL}
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

// spoolsPost calls POST {apiBase}/api/upload with the "action"-dispatched
// envelope api/upload/index.go expects, same shape the web dashboard sends.
func spoolsPost(cfg config, action string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	envelope["action"] = action
	body, err = json.Marshal(envelope)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.apiBase+"/api/upload", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiToken)

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
