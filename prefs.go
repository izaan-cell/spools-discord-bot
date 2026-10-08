package main

import (
	"encoding/json"
	"os"
	"sync"
)

// prefStore is a tiny JSON-file-backed per-user setting: whether the bot
// should ask before uploading an attachment it sees from them. Default is
// true (on) when a user has no entry yet, matching "ask by default" from
// the product requirement - only an explicit /autoprompt off writes false.
//
// A plain file instead of a real database on purpose: this bot runs on a
// third-party free host, nothing here is sensitive, and the whole
// deployment is meant to be one self-contained binary with no external
// service to configure. Survives a simple restart; a full redeploy that
// re-clones the repo from scratch will reset it, since this file isn't
// part of the repo - acceptable for a personal-server bot's one setting.
type prefStore struct {
	mu   sync.Mutex
	path string
	data map[string]bool
}

func loadPrefs(path string) *prefStore {
	p := &prefStore{path: path, data: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	json.Unmarshal(raw, &p.data)
	return p
}

func (p *prefStore) autoPromptEnabled(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	enabled, ok := p.data[userID]
	if !ok {
		return true
	}
	return enabled
}

func (p *prefStore) setAutoPrompt(userID string, enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data[userID] = enabled
	raw, err := json.Marshal(p.data)
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, raw, 0o644)
}
