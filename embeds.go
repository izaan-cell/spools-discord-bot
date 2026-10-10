package main

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Spools' own brand colors (src/app.css: --accent / --panel), so embeds
// look like they belong to the same product instead of Discord's default
// blurple.
const (
	colorAccent = 0xf5b498
	colorError  = 0xe8a598
)

// occasionalFooter is the plain "spools.studio" footer most of the time,
// and a small Amber upsell line roughly one command in five - mirrors the
// website's free-tier ad slots (ads for everyone who isn't paying, never
// for Amber+), translated to "an occasional line in the footer" since a
// bot has no real ad inventory to sell. Used on the general-purpose
// commands (/help, /dashboard, /search, /amber); left off the upload
// ask/result embeds since their footer text is functional instruction,
// not branding, and overwriting it would hurt usability.
func occasionalFooter() *discordgo.MessageEmbedFooter {
	if rand.Intn(5) == 0 {
		return &discordgo.MessageEmbedFooter{
			Text: "spools.studio · Go Amber for bigger uploads, more slots, and zero ads — /amber",
		}
	}
	return &discordgo.MessageEmbedFooter{Text: "spools.studio"}
}

func welcomeEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "👋 Thanks for adding Spools Uploader",
		Description: "Post an image, GIF, or video in any channel I can see, and I'll offer to upload it " +
			"to **Spools** and hand back a shareable link - no need to leave Discord.\n\n" +
			"Run **/help** any time for the full rundown.",
		Color: colorAccent,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "spools.studio",
		},
	}
}

func helpEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Spools Uploader",
		Description: "Simple image, GIF, and video hosting, from inside Discord.",
		Color:       colorAccent,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "How it works",
				Value: "Post an image, GIF, or video attachment anywhere I can see, and I'll ask if you " +
					"want it uploaded to Spools. Click **Upload** and I'll reply with the link.",
			},
			{
				Name: "Commands",
				Value: "**/help** - show this message\n" +
					"**/upload file:<attachment>** - upload a file directly, no prompt needed\n" +
					"**/edit upload:<link or key>** - rename or redescribe something you uploaded\n" +
					"**/autoprompt enabled:<true/false>** - turn the upload prompt on or off for yourself\n" +
					"**/search query:<text>** - search public Spools uploads\n" +
					"**/dashboard** - link to your Spools dashboard\n" +
					"**/amber** - what Spools' paid tiers get you\n" +
					"**/link** (DM only) - connect your own Amber account\n" +
					"**/unlink** (DM only) - disconnect it",
			},
			{
				Name:  "Prompts are on by default",
				Value: "Every account starts with prompts on. Run `/autoprompt enabled:false` any time to stop being asked.",
			},
		},
		Footer: occasionalFooter(),
	}
}

func dashboardEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "Your Spools Dashboard",
		Description: "Manage everything you've uploaded, update your profile, check billing, and more:\n\n" +
			"**[Open your dashboard](https://spools.studio/dashboard)**",
		Color:  colorAccent,
		Footer: occasionalFooter(),
	}
}

func amberEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Go Amber",
		Description: "Spools' paid tiers: bigger upload slots, bigger file size caps, a tier badge, priority moderation, shorter usernames, deeper analytics, collections, personal API access, and zero ads.",
		Color:       colorAccent,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Amber", Value: "More slots and bigger size caps than Free", Inline: true},
			{Name: "Amber Gold", Value: "More of everything Amber gets you", Inline: true},
			{Name: "Amber Prime", Value: "The full set, top size caps", Inline: true},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "See exact pricing and perks at spools.studio/subscriptions",
		},
	}
}

// linkGuideEmbed walks an Amber user through getting a token and linking
// it, as one self-contained set of steps rather than a separate help
// command - shown when /link is run with no token yet.
func linkGuideEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "Connect your Spools account",
		Description: "This is an Amber+ perk - your account needs a paid tier to generate an API token. " +
			"Once linked, your uploads go through your own Spools account instead of the shared bot account.",
		Color: colorAccent,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "1. Get a token",
				Value: "Go to **[spools.studio/dashboard/api](https://spools.studio/dashboard/api)** " +
					"and create a new API token. Copy it - it's only shown once.",
			},
			{
				Name:  "2. Link it",
				Value: "Come back here and run `/link token:` followed by the token you copied.",
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Not Amber yet? Run /amber to see what it gets you.",
		},
	}
}

func searchResultsEmbed(query string, items []searchItem, apiBase string) *discordgo.MessageEmbed {
	if len(items) == 0 {
		return &discordgo.MessageEmbed{
			Title:       "No results",
			Description: fmt.Sprintf("Nothing public on Spools matches “%s”.", query),
			Color:       colorAccent,
			Footer:      occasionalFooter(),
		}
	}

	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("• [%s](%s%s) — %d view%s", item.Filename, apiBase, item.URL, item.ViewCount, plural(item.ViewCount))
	}

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Search: %s", query),
		Description: strings.Join(lines, "\n"),
		Color:       colorAccent,
		Footer:      occasionalFooter(),
	}
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func askEmbed(attachments []*discordgo.MessageAttachment, pendingID string) (*discordgo.MessageEmbed, discordgo.ActionsRow) {
	names := make([]string, len(attachments))
	for i, a := range attachments {
		names[i] = "• " + a.Filename
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Upload to Spools?",
		Description: strings.Join(names, "\n"),
		Color:       colorAccent,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Run /autoprompt enabled:false to turn off automatic asking",
		},
	}

	label := "Upload"
	if len(attachments) > 1 {
		label = fmt.Sprintf("Upload (%d)", len(attachments))
	}

	row := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				Label:    label,
				Style:    discordgo.SuccessButton,
				CustomID: pendingID,
			},
		},
	}
	return embed, row
}

func resultEmbed(results []uploadResult) *discordgo.MessageEmbed {
	failed := 0
	lines := make([]string, len(results))
	for i, r := range results {
		if r.Err != nil {
			failed++
			lines[i] = fmt.Sprintf("❌ %s — %s", r.Filename, r.Err.Error())
		} else {
			lines[i] = fmt.Sprintf("✅ [%s](%s)", r.Filename, r.Link)
		}
	}

	title := "Uploaded to Spools"
	color := colorAccent
	switch {
	case failed == len(results):
		title = "Upload failed"
		color = colorError
	case failed > 0:
		title = "Some uploads failed"
		color = colorError
	}

	return &discordgo.MessageEmbed{
		Title:       title,
		Description: strings.Join(lines, "\n"),
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "spools.studio",
		},
	}
}
