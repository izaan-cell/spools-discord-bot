package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// uptimeCheckRequest mirrors api/status's POST body in the main Spools repo.
type uptimeCheckRequest struct {
	Target     string `json:"target"`
	IsUp       bool   `json:"isUp"`
	ResponseMs *int   `json:"responseMs"`
	StatusCode *int   `json:"statusCode"`
}

// runUptimeChecks pings the live site every 5 minutes and reports results to
// api/status - real uptime history for the /status page, replacing its old
// "no monitoring exists" disclaimer. Reuses this bot's existing always-on
// process and its existing bot secret rather than adding a new service or
// credential. "Up" means a response came back at all within the timeout,
// any status code - only a timeout or connection failure counts as down,
// same definition every uptime monitor uses.
func runUptimeChecks(cfg config) {
	targets := map[string]string{
		"website": "https://www.spools.studio/",
		"api":     "https://api.spools.studio/account",
	}

	client := &http.Client{Timeout: 15 * time.Second}

	check := func(target, url string) {
		start := time.Now()
		resp, err := client.Get(url)
		elapsedMs := int(time.Since(start).Milliseconds())

		req := uptimeCheckRequest{Target: target, ResponseMs: &elapsedMs}
		if err != nil {
			req.IsUp = false
		} else {
			resp.Body.Close()
			req.IsUp = true
			code := resp.StatusCode
			req.StatusCode = &code
		}

		if err := reportUptimeCheck(cfg, req); err != nil {
			log.Printf("could not report uptime check for %s: %v", target, err)
		}
	}

	for target, url := range targets {
		check(target, url)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		for target, url := range targets {
			check(target, url)
		}
	}
}

// reportUptimeCheck POSTs to api/status directly (not through botAPI, which
// hardcodes the /api/discord-bot path) - same bot secret, different
// endpoint.
func reportUptimeCheck(cfg config, body uptimeCheckRequest) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.apiBase+"/api/status", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.botSecret)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// client.Do only errors on network/connection failures, never on an
	// HTTP error status - without this check, a rejected request (bad
	// secret, bad payload, server error) looked like silent success: no
	// error logged, no row inserted, no visible sign anything was wrong.
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("api/status responded with status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// runBotSelfReport reports "the bot is alive and connected" every 5 minutes,
// started only once Discord's READY event has actually fired. There's no
// external way to ping the bot itself the way website/api are pinged - if
// this process dies, these reports simply stop arriving, and api/status
// marks the target stale past ~15 minutes of silence rather than trusting
// a last-known isUp:true forever.
func runBotSelfReport(cfg config) {
	report := func() {
		if err := reportUptimeCheck(cfg, uptimeCheckRequest{Target: "bot", IsUp: true}); err != nil {
			log.Printf("could not report bot self-check: %v", err)
		}
	}
	report()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		report()
	}
}
