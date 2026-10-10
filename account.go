package main

// Thin wrappers over botAPI (upload.go) for every non-upload action
// api/discord-bot exposes. No local state on the bot's own side for any of
// this anymore - linking, prefs, and ownership all live in Supabase now.

func linkAccount(cfg config, discordUserID, token string) (username string, err error) {
	var resp struct {
		Username string `json:"username"`
	}
	err = botAPI(cfg, "link", map[string]any{
		"discordUserId": discordUserID,
		"token":         token,
	}, &resp)
	return resp.Username, err
}

func unlinkAccount(cfg config, discordUserID string) (wasLinked bool, err error) {
	var resp struct {
		WasLinked bool `json:"wasLinked"`
	}
	err = botAPI(cfg, "unlink", map[string]any{
		"discordUserId": discordUserID,
	}, &resp)
	return resp.WasLinked, err
}

// autoPromptEnabled defaults to true if the call fails - fail open toward
// asking, so a transient backend hiccup shows up as "the bot is asking
// again" (visible, harmless) rather than "the bot silently stopped
// responding" (confusing, looks broken).
func autoPromptEnabled(cfg config, discordUserID string) bool {
	var resp struct {
		AutoPrompt bool `json:"autoPrompt"`
	}
	if err := botAPI(cfg, "get-prefs", map[string]any{"discordUserId": discordUserID}, &resp); err != nil {
		return true
	}
	return resp.AutoPrompt
}

func setAutoPrompt(cfg config, discordUserID string, enabled bool) error {
	return botAPI(cfg, "set-prefs", map[string]any{
		"discordUserId": discordUserID,
		"autoPrompt":    enabled,
	}, nil)
}

func editMedia(cfg config, discordUserID, key string, filename, description *string) error {
	return botAPI(cfg, "edit", map[string]any{
		"discordUserId": discordUserID,
		"key":           key,
		"filename":      filename,
		"description":   description,
	}, nil)
}
