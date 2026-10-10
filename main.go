// Standalone Go process, not a Vercel function - this one needs a real
// persistent connection to Discord's gateway (a websocket), which a
// request/response serverless function can't hold open. Deployed to its own
// host (not Vercel), as its own Go module with nothing else attached - see
// upload.go's comment for why the extension/content-type rules are
// duplicated here instead of importing the main Spools repo's mediarules
// package.
//
// Watches every channel it can see, across whatever server(s) it's invited
// to (global slash commands, no guild-specific setup). On a message with an
// image/gif/video attachment, it asks (via an embed + button, which opens a
// description modal) before uploading - unless that user has turned prompts
// off with /autoprompt. Every account-specific thing (linking, prefs,
// ownership) is resolved server-side by api/discord-bot in the main Spools
// repo; this process holds no state of its own beyond in-flight pending
// uploads, and never sees a Spools API token or DATABASE_URL, only the one
// shared DISCORD_BOT_SECRET.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

type config struct {
	discordToken string
	apiBase      string
	botSecret    string
}

func loadConfig() (config, error) {
	cfg := config{
		discordToken: os.Getenv("DISCORD_BOT_TOKEN"),
		apiBase:      strings.TrimSuffix(os.Getenv("SPOOLS_API_BASE"), "/"),
		botSecret:    os.Getenv("DISCORD_BOT_SECRET"),
	}
	if cfg.apiBase == "" {
		cfg.apiBase = "https://spools.studio"
	}
	missing := []string{}
	if cfg.discordToken == "" {
		missing = append(missing, "DISCORD_BOT_TOKEN")
	}
	if cfg.botSecret == "" {
		missing = append(missing, "DISCORD_BOT_SECRET")
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

	pending := newPendingStore()
	go func() {
		for range time.Tick(10 * time.Minute) {
			pending.sweepStale(30 * time.Minute)
		}
	}()

	session, err := discordgo.New("Bot " + cfg.discordToken)
	if err != nil {
		log.Fatalf("could not create discord session: %v", err)
	}
	// MessageContent is a privileged intent - must also be switched on for
	// this bot under Settings -> Bot in the Discord developer portal, no
	// approval needed below 100 servers.
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentMessageContent

	// Discord replays every guild the bot is already in as a GuildCreate
	// event right after Ready, with no field distinguishing that from a
	// genuinely new join. The common workaround: only treat GuildCreate as
	// "just joined" once a few seconds have passed since Ready, by which
	// point that replay has finished and anything arriving after is real.
	var ready atomic.Bool
	// session.Open() returning only means the websocket handshake started,
	// not that Discord's READY dispatch (where the bot actually becomes
	// usable) has arrived yet - closed once, in the Ready handler below, so
	// main() can block on it instead of just assuming it already happened.
	readyCh := make(chan struct{})

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("READY event received, logged in as %s", r.User.String())
		if err := registerCommands(s); err != nil {
			log.Printf("could not register slash commands: %v", err)
		}
		time.AfterFunc(5*time.Second, func() { ready.Store(true) })
		close(readyCh)
	})

	session.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		if !ready.Load() {
			return // part of the startup sync, not a new join
		}
		sendWelcome(s, g.Guild)
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || len(m.Attachments) == 0 {
			return
		}
		go handleMessage(s, cfg, pending, m)
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		handleInteraction(s, i, cfg, pending)
	})

	if err := session.Open(); err != nil {
		log.Fatalf("could not connect to discord: %v", err)
	}
	defer session.Close()

	log.Println("websocket open, waiting for Discord's READY event...")
	select {
	case <-readyCh:
		log.Printf("watching all channels, posting to %s", cfg.apiBase)
	case <-time.After(20 * time.Second):
		log.Println("WARNING: no READY event received within 20s - connected but the gateway handshake may be stuck; slash commands will not be registered until it arrives")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
}

// sendWelcome tries the server's configured system channel first (the same
// one Discord's own "X joined the server" messages use), then falls back to
// the first text channel the bot can actually post in.
func sendWelcome(s *discordgo.Session, g *discordgo.Guild) {
	embed := welcomeEmbed()

	if g.SystemChannelID != "" {
		if _, err := s.ChannelMessageSendEmbed(g.SystemChannelID, embed); err == nil {
			return
		}
	}

	for _, ch := range g.Channels {
		if ch.Type != discordgo.ChannelTypeGuildText {
			continue
		}
		if _, err := s.ChannelMessageSendEmbed(ch.ID, embed); err == nil {
			return
		}
	}
	log.Printf("could not find a postable channel to welcome guild %s", g.ID)
}

func handleMessage(s *discordgo.Session, cfg config, pending *pendingStore, m *discordgo.MessageCreate) {
	var uploadable []*discordgo.MessageAttachment
	for _, att := range m.Attachments {
		if _, _, ok := categoryFor(att.Filename); ok {
			uploadable = append(uploadable, att)
		}
	}
	if len(uploadable) == 0 {
		return
	}

	// One network round trip to the backend per qualifying message - fine
	// at this bot's scale, and keeps "on by default" authoritative in one
	// place (Supabase) instead of a local cache that could drift from it.
	if !autoPromptEnabled(cfg, m.Author.ID) {
		return
	}

	id := pending.add(m.Author.ID, uploadable)
	embed, row := askEmbed(uploadable, id)

	_, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: []discordgo.MessageComponent{row},
		Reference:  m.Reference(),
	})
	if err != nil {
		log.Printf("could not send upload prompt: %v", err)
	}
}
