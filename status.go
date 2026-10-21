package main

import (
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

// statsResponse mirrors api/discord-bot's "stats" action in the main Spools
// repo (api/discord-bot/index.go's handleStats) - just a total upload count,
// no per-user data.
type statsResponse struct {
	TotalUploads int64 `json:"totalUploads"`
}

// runStatusRotation cycles the bot's "Watching ..." presence through a fixed
// set of messages. Server/user counts come free from the gateway's own
// cached guild state (no API call); the Spools-wide upload count needs a
// network round trip, so it's only refreshed every few rotations rather than
// on every tick.
func runStatusRotation(s *discordgo.Session, cfg config) {
	var totalUploads int64
	refreshUploadCount := func() {
		var out statsResponse
		if err := botAPI(cfg, "stats", map[string]any{}, &out); err != nil {
			log.Printf("could not refresh upload count for status: %v", err)
			return
		}
		totalUploads = out.TotalUploads
	}
	refreshUploadCount()

	statuses := []func() string{
		func() string { return "/help" },
		func() string { return "/upload" },
		func() string {
			guilds := len(s.State.Guilds)
			var users int
			for _, g := range s.State.Guilds {
				users += g.MemberCount
			}
			return fmt.Sprintf("%d servers, %d users", guilds, users)
		},
		func() string { return "spools.studio" },
		func() string { return fmt.Sprintf("%d uploads on Spools", totalUploads) },
	}

	i := 0
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Refresh the one status that needs a network call roughly once per
		// full lap through the list, not on every single tick.
		if i%len(statuses) == 0 {
			refreshUploadCount()
		}

		text := statuses[i%len(statuses)]()
		err := s.UpdateStatusComplex(discordgo.UpdateStatusData{
			Activities: []*discordgo.Activity{
				{Name: text, Type: discordgo.ActivityTypeWatching},
			},
		})
		if err != nil {
			log.Printf("could not update status: %v", err)
		}
		i++
	}
}
