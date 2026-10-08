// Standalone Go process, not a Vercel function - this one needs a real
// persistent connection to Discord's gateway (a websocket), which a
// request/response serverless function can't hold open. Deployed to its own
// host (not Vercel), as its own Go module with nothing else attached -
// deliberately NOT importing api/_lib/mediarules from the main repo module,
// even though it duplicates that package's tiny extension/content-type
// table, so this directory can be uploaded to a third-party host on its own
// without dragging the rest of Spools' private source along with it. The
// extension list below is cosmetic (a pre-check for a nicer error message);
// api/upload's own mediarules.ValidateForTier is what actually enforces it,
// so a stale copy here can never let something invalid through.
//
// Watches every channel it can see (not scoped to one), across whatever
// server(s) it's invited to. Any message with an image/gif/video attachment
// gets pushed through the exact same presign -> PUT -> confirm flow the web
// dashboard uses (api/upload), authenticated with a personal API token
// (spt_..., minted at /dashboard/api), then replies in-channel with the
// resulting Spools link.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Mirrors api/_lib/mediarules.Rules/ContentTypes (not imported - see the
// package comment above). Keep in sync by hand if those ever change.
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

type config struct {
	discordToken string
	apiBase      string
	apiToken     string
}

func loadConfig() (config, error) {
	cfg := config{
		discordToken: os.Getenv("DISCORD_BOT_TOKEN"),
		apiBase:      strings.TrimSuffix(os.Getenv("SPOOLS_API_BASE"), "/"),
		apiToken:     os.Getenv("SPOOLS_API_TOKEN"),
	}
	if cfg.apiBase == "" {
		cfg.apiBase = "https://spools.studio"
	}
	missing := []string{}
	if cfg.discordToken == "" {
		missing = append(missing, "DISCORD_BOT_TOKEN")
	}
	if cfg.apiToken == "" {
		missing = append(missing, "SPOOLS_API_TOKEN")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	session, err := discordgo.New("Bot " + cfg.discordToken)
	if err != nil {
		log.Fatalf("could not create discord session: %v", err)
	}
	// MessageContent is a privileged intent - must also be switched on for
	// this bot under Settings -> Bot in the Discord developer portal, no
	// approval needed below 100 servers.
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || len(m.Attachments) == 0 {
			return
		}
		// Handled off the gateway's event goroutine so a slow upload never
		// delays heartbeats/other events.
		go handleMessage(s, cfg, m)
	})

	if err := session.Open(); err != nil {
		log.Fatalf("could not connect to discord: %v", err)
	}
	defer session.Close()

	log.Printf("watching all channels, posting to %s", cfg.apiBase)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
}

func handleMessage(s *discordgo.Session, cfg config, m *discordgo.MessageCreate) {
	for _, att := range m.Attachments {
		category, ext, ok := categoryFor(att.Filename)
		if !ok {
			continue
		}

		link, err := uploadAttachment(cfg, att, category, ext)
		if err != nil {
			reply(s, m, fmt.Sprintf("couldn't upload %s: %v", att.Filename, err))
			continue
		}
		reply(s, m, fmt.Sprintf("uploaded %s → %s", att.Filename, link))
	}
}

func reply(s *discordgo.Session, m *discordgo.MessageCreate, content string) {
	if _, err := s.ChannelMessageSendReply(m.ChannelID, content, m.Reference()); err != nil {
		log.Printf("could not send reply: %v", err)
	}
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

func uploadAttachment(cfg config, att *discordgo.MessageAttachment, category, ext string) (string, error) {
	contentType := extensionContentType[ext]
	if contentType == "" {
		contentType = att.ContentType
	}

	data, err := downloadAttachment(att.URL)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	var presigned presignResponse
	err = spoolsPost(cfg, "presign", presignRequest{
		Filename:    att.Filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Category:    category,
	}, &presigned)
	if err != nil {
		return "", fmt.Errorf("presign failed: %w", err)
	}

	if err := putObject(presigned.UploadURL, contentType, data); err != nil {
		return "", fmt.Errorf("upload to storage failed: %w", err)
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
		return "", fmt.Errorf("confirm failed: %w", err)
	}

	return cfg.apiBase + confirmed.URL, nil
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
