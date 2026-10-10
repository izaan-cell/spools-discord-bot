package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var slashCommands = []*discordgo.ApplicationCommand{
	{
		Name:        "help",
		Description: "Show what this bot does and how to use it",
	},
	{
		Name:        "autoprompt",
		Description: "Turn the upload prompt on or off for yourself",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "enabled",
				Description: "Ask before uploading your attachments?",
				Required:    true,
			},
		},
	},
	{
		Name:        "upload",
		Description: "Upload an image, GIF, or video to Spools directly",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionAttachment,
				Name:        "file",
				Description: "The file to upload",
				Required:    true,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "description",
				Description: "Optional note to attach to this upload",
				Required:    false,
			},
		},
	},
	{
		Name:        "edit",
		Description: "Rename or redescribe something you uploaded",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "upload",
				Description: "The Spools link or key of the upload to edit",
				Required:    true,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "New filename (optional)",
				Required:    false,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "description",
				Description: "New description (optional)",
				Required:    false,
			},
		},
	},
	{
		Name:        "search",
		Description: "Search public uploads on Spools",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "query",
				Description: "Filename to search for",
				Required:    true,
			},
		},
	},
	{
		Name:        "dashboard",
		Description: "Get a link to your Spools dashboard",
	},
	{
		Name:        "amber",
		Description: "See what Spools' paid tiers get you",
	},
	{
		Name:        "link",
		Description: "Connect your own Amber Spools account, so uploads go under your name instead of the shared bot account",
		// DM-only: linking means pasting a real credential, which must never
		// be typeable in a public channel where anyone could read it back.
		Contexts: &[]discordgo.InteractionContextType{discordgo.InteractionContextBotDM},
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "token",
				Description: "Your Spools API token - leave this empty to see how to get one first",
				Required:    false,
			},
		},
	},
	{
		Name:        "unlink",
		Description: "Disconnect your linked Spools account - uploads go back through the shared bot account",
		Contexts:    &[]discordgo.InteractionContextType{discordgo.InteractionContextBotDM},
	},
}

// registerCommands is called once on Ready. Global (no guild ID) so the
// bot works in any server it's invited to without per-server setup, same
// "global, not testing" scope as everything else about this bot - global
// commands take up to an hour to propagate on first registration, which is
// a one-time cost, not a per-restart one (Discord caches them).
func registerCommands(s *discordgo.Session) error {
	if s.State == nil || s.State.User == nil || s.State.User.ID == "" {
		return fmt.Errorf("session has no authenticated user yet")
	}
	created, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", slashCommands)
	if err != nil {
		return err
	}
	names := make([]string, len(created))
	for i, c := range created {
		names[i] = "/" + c.Name
	}
	log.Printf("registered %d slash commands: %s", len(created), strings.Join(names, ", "))
	return nil
}

// pendingUpload is one ask-embed's worth of attachments, from the button
// click (which opens a description modal, see handleButtonClick) through to
// the modal submit (which actually uploads, see handleModalSubmit). Kept in
// memory only - a bot restart between the two just means the modal submit
// fails with "expired", not a lost upload (nothing was ever committed).
type pendingUpload struct {
	authorID    string
	attachments []*discordgo.MessageAttachment
	createdAt   time.Time
}

type pendingStore struct {
	mu    sync.Mutex
	items map[string]*pendingUpload
}

func newPendingStore() *pendingStore {
	return &pendingStore{items: map[string]*pendingUpload{}}
}

func (p *pendingStore) add(authorID string, attachments []*discordgo.MessageAttachment) string {
	id := randomID()
	p.mu.Lock()
	p.items[id] = &pendingUpload{authorID: authorID, attachments: attachments, createdAt: time.Now()}
	p.mu.Unlock()
	return id
}

// peek looks up a pending upload without consuming it - used by the button
// click, which only needs to validate ownership before opening a modal, not
// upload anything yet.
func (p *pendingStore) peek(id string) (*pendingUpload, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.items[id]
	return item, ok
}

func (p *pendingStore) take(id string) (*pendingUpload, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.items[id]
	if ok {
		delete(p.items, id)
	}
	return item, ok
}

// sweepStale drops anything nobody ever clicked/submitted, so a long-running
// process doesn't slowly accumulate dead entries from ignored prompts.
func (p *pendingStore) sweepStale(maxAge time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, item := range p.items {
		if time.Since(item.createdAt) > maxAge {
			delete(p.items, id)
		}
	}
}

func randomID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// handleInteraction dispatches slash commands, the "Upload" button click,
// and the description modal it opens - all three arrive as InteractionCreate
// events, distinguished by Type.
func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, pending *pendingStore) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		handleSlashCommand(s, i, cfg)
	case discordgo.InteractionMessageComponent:
		handleButtonClick(s, i, pending)
	case discordgo.InteractionModalSubmit:
		handleModalSubmit(s, i, cfg, pending)
	}
}

func handleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config) {
	data := i.ApplicationCommandData()
	switch data.Name {
	case "help":
		respond(s, i, &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{helpEmbed()}})
	case "autoprompt":
		enabled := data.GetOption("enabled").BoolValue()
		userID := interactionUserID(i)
		if err := setAutoPrompt(cfg, userID, enabled); err != nil {
			respondEphemeral(s, i, "Couldn't save that, try again in a moment.")
			return
		}
		state := "on"
		if !enabled {
			state = "off"
		}
		respondEphemeral(s, i, fmt.Sprintf("Upload prompts are now **%s** for you.", state))
	case "upload":
		handleUploadCommand(s, i, cfg, data)
	case "edit":
		handleEditCommand(s, i, cfg, data)
	case "search":
		handleSearchCommand(s, i, cfg, data)
	case "dashboard":
		respond(s, i, &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{dashboardEmbed()}})
	case "amber":
		respond(s, i, &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{amberEmbed()}})
	case "link":
		handleLinkCommand(s, i, cfg, data)
	case "unlink":
		userID := interactionUserID(i)
		wasLinked, err := unlinkAccount(cfg, userID)
		if err != nil {
			respondEphemeral(s, i, fmt.Sprintf("Couldn't unlink: %s", err.Error()))
			return
		}
		if wasLinked {
			respondEphemeral(s, i, "Unlinked. Your uploads will go through the shared Spools bot account again.")
		} else {
			respondEphemeral(s, i, "You don't have an account linked.")
		}
	}
}

// handleLinkCommand is deliberately a single command rather than a
// "/link-help" + "/link" pair: running it bare shows the guided steps,
// running it with a token validates and saves - one thing to remember, not
// two. DM-only (enforced by the command's Contexts), so a pasted token is
// never visible in a public channel. Validation itself now happens
// server-side (api/discord-bot's link action) - this is just the Discord
// side of that call.
func handleLinkCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, data discordgo.ApplicationCommandInteractionData) {
	opt := data.GetOption("token")
	if opt == nil || strings.TrimSpace(opt.StringValue()) == "" {
		respond(s, i, &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{linkGuideEmbed()}})
		return
	}
	token := strings.TrimSpace(opt.StringValue())

	// Validating is a network call, acknowledge first.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("could not ack /link: %v", err)
		return
	}

	username, err := linkAccount(cfg, interactionUserID(i), token)
	if err != nil {
		editEphemeral(s, i, fmt.Sprintf("Couldn't link that account: %s", err.Error()))
		return
	}
	editEphemeral(s, i, fmt.Sprintf("Linked as **@%s**. Your uploads now go through your own Spools account.", username))
}

// handleUploadCommand is /upload's handler: unlike the passive flow, this
// one skips the ask-embed/modal entirely - running the command with its
// optional description argument already filled in is itself the explicit
// confirmation, so it uploads immediately and reports the result.
func handleUploadCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, data discordgo.ApplicationCommandInteractionData) {
	opt := data.GetOption("file")
	if opt == nil {
		respondEphemeral(s, i, "No file was attached.")
		return
	}
	attachmentID, ok := opt.Value.(string)
	if !ok || data.Resolved == nil {
		respondEphemeral(s, i, "Couldn't read that attachment, try again.")
		return
	}
	att, ok := data.Resolved.Attachments[attachmentID]
	if !ok {
		respondEphemeral(s, i, "Couldn't read that attachment, try again.")
		return
	}
	description := ""
	if descOpt := data.GetOption("description"); descOpt != nil {
		description = strings.TrimSpace(descOpt.StringValue())
	}

	// Uploads can take a few seconds; acknowledge immediately (Discord
	// requires a response within 3s) and edit with the real result once done.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("could not ack /upload: %v", err)
		return
	}

	result := uploadAttachment(cfg, interactionUserID(i), att, description)
	embed := resultEmbed([]uploadResult{result})
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	}); err != nil {
		log.Printf("could not edit /upload result: %v", err)
	}
}

// handleEditCommand resolves a pasted link or bare key down to just the
// key (everything after the last "/"), then defers to api/discord-bot's
// edit action, which enforces ownership server-side.
func handleEditCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, data discordgo.ApplicationCommandInteractionData) {
	rawKey := strings.TrimSpace(data.GetOption("upload").StringValue())
	key := rawKey
	if idx := strings.LastIndex(rawKey, "/"); idx != -1 {
		key = rawKey[idx+1:]
	}
	if key == "" {
		respondEphemeral(s, i, "That doesn't look like a Spools link or key.")
		return
	}

	var filename, description *string
	if opt := data.GetOption("name"); opt != nil {
		v := strings.TrimSpace(opt.StringValue())
		filename = &v
	}
	if opt := data.GetOption("description"); opt != nil {
		v := strings.TrimSpace(opt.StringValue())
		description = &v
	}
	if filename == nil && description == nil {
		respondEphemeral(s, i, "Give me at least a new name or description to change.")
		return
	}

	if err := editMedia(cfg, interactionUserID(i), key, filename, description); err != nil {
		respondEphemeral(s, i, fmt.Sprintf("Couldn't update that: %s", err.Error()))
		return
	}
	respondEphemeral(s, i, "Updated.")
}

func handleSearchCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, data discordgo.ApplicationCommandInteractionData) {
	query := strings.TrimSpace(data.GetOption("query").StringValue())

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("could not ack /search: %v", err)
		return
	}

	items, err := searchDiscover(cfg, query, 8)
	if err != nil {
		editEphemeral(s, i, fmt.Sprintf("Couldn't search Spools right now: %s", err.Error()))
		return
	}
	embed := searchResultsEmbed(query, items, cfg.apiBase)
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	}); err != nil {
		log.Printf("could not edit /search result: %v", err)
	}
}

// handleButtonClick opens a description modal rather than uploading
// immediately - it only peeks the pending upload (doesn't consume it) since
// the modal submit is what actually does the work, and the button click
// might never be followed by a submit (modal dismissed).
func handleButtonClick(s *discordgo.Session, i *discordgo.InteractionCreate, pending *pendingStore) {
	data := i.MessageComponentData()
	item, ok := pending.peek(data.CustomID)
	if !ok {
		respondEphemeral(s, i, "This upload prompt expired, post the file again to get a new one.")
		return
	}
	if interactionUserID(i) != item.authorID {
		respondEphemeral(s, i, "This isn't your upload prompt.")
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: data.CustomID,
			Title:    "Upload to Spools",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "description",
						Label:       "Description (optional)",
						Style:       discordgo.TextInputParagraph,
						Required:    false,
						MaxLength:   500,
						Placeholder: "Add a note about this upload…",
					},
				}},
			},
		},
	}); err != nil {
		log.Printf("could not open upload modal: %v", err)
	}
}

func handleModalSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, pending *pendingStore) {
	data := i.ModalSubmitData()
	item, ok := pending.take(data.CustomID)
	if !ok {
		respondEphemeral(s, i, "This upload prompt expired, post the file again to get a new one.")
		return
	}
	if interactionUserID(i) != item.authorID {
		respondEphemeral(s, i, "This isn't your upload prompt.")
		return
	}

	description := ""
	if row, ok := data.Components[0].(*discordgo.ActionsRow); ok && len(row.Components) > 0 {
		if input, ok := row.Components[0].(*discordgo.TextInput); ok {
			description = strings.TrimSpace(input.Value)
		}
	}

	// Uploads can take a few seconds; acknowledge immediately and follow up.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("could not ack modal submit: %v", err)
		return
	}

	userID := interactionUserID(i)
	results := make([]uploadResult, len(item.attachments))
	for idx, att := range item.attachments {
		results[idx] = uploadAttachment(cfg, userID, att, description)
	}

	embed := resultEmbed(results)
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	}); err != nil {
		log.Printf("could not edit upload result: %v", err)
	}
}

func interactionUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, data *discordgo.InteractionResponseData) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}); err != nil {
		log.Printf("could not respond to interaction: %v", err)
	}
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	respond(s, i, &discordgo.InteractionResponseData{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

// editEphemeral follows up a deferred response (see
// InteractionResponseDeferredChannelMessageWithSource call sites above)
// with plain text - used for /link and /search, whose results aren't known
// until after a network call completes. Visibility (ephemeral or not) was
// already set on the initial deferred ack, editing can't change it.
func editEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content}); err != nil {
		log.Printf("could not edit interaction response: %v", err)
	}
}
