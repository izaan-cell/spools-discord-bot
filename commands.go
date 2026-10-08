package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
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
}

// registerCommands is called once on Ready. Global (no guild ID) so the
// bot works in any server it's invited to without per-server setup, same
// "global, not testing" scope as everything else about this bot - global
// commands take up to an hour to propagate on first registration, which is
// a one-time cost, not a per-restart one (Discord caches them).
func registerCommands(s *discordgo.Session) error {
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", slashCommands)
	return err
}

// pendingUpload is one ask-embed's worth of attachments, waiting on its
// button to be clicked. Kept in memory only (see prefs.go's comment on why
// that's an acceptable tradeoff here) and swept for staleness below.
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

func (p *pendingStore) take(id string) (*pendingUpload, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.items[id]
	if ok {
		delete(p.items, id)
	}
	return item, ok
}

// sweepStale drops anything nobody ever clicked, so a long-running process
// doesn't slowly accumulate dead entries from ignored prompts.
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

// handleInteraction dispatches both slash commands and the "Upload" button
// clicks they lead to - Discord sends both as InteractionCreate events.
func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, prefs *prefStore, pending *pendingStore) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		handleSlashCommand(s, i, prefs)
	case discordgo.InteractionMessageComponent:
		handleButtonClick(s, i, cfg, pending)
	}
}

func handleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate, prefs *prefStore) {
	data := i.ApplicationCommandData()
	switch data.Name {
	case "help":
		respond(s, i, &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{helpEmbed()}})
	case "autoprompt":
		enabled := data.GetOption("enabled").BoolValue()
		userID := interactionUserID(i)
		if err := prefs.setAutoPrompt(userID, enabled); err != nil {
			respondEphemeral(s, i, "Couldn't save that, try again in a moment.")
			return
		}
		state := "on"
		if !enabled {
			state = "off"
		}
		respondEphemeral(s, i, fmt.Sprintf("Upload prompts are now **%s** for you.", state))
	}
}

func handleButtonClick(s *discordgo.Session, i *discordgo.InteractionCreate, cfg config, pending *pendingStore) {
	data := i.MessageComponentData()
	item, ok := pending.take(data.CustomID)
	if !ok {
		respondEphemeral(s, i, "This upload prompt expired, post the file again to get a new one.")
		return
	}
	if interactionUserID(i) != item.authorID {
		respondEphemeral(s, i, "This isn't your upload prompt.")
		return
	}

	// Uploads can take a few seconds; acknowledge immediately (Discord
	// requires a response within 3s) and edit the message once done.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		log.Printf("could not ack button click: %v", err)
		return
	}

	results := make([]uploadResult, len(item.attachments))
	for idx, att := range item.attachments {
		results[idx] = uploadAttachment(cfg, att)
	}

	embed := resultEmbed(results)
	noComponents := []discordgo.MessageComponent{}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds:     &[]*discordgo.MessageEmbed{embed},
		Components: &noComponents,
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
