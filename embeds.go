package main

import (
	"fmt"
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
					"**/autoprompt enabled:<true/false>** - turn the upload prompt on or off for yourself",
			},
			{
				Name:  "Prompts are on by default",
				Value: "Every account starts with prompts on. Run `/autoprompt enabled:false` any time to stop being asked.",
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "spools.studio",
		},
	}
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
