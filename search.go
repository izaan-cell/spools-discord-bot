package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// searchItem mirrors the fields api/media-info's handleList returns that
// this bot actually uses - a subset of the real mediaListItem struct in the
// main Spools repo (not imported, same standalone-module reasoning as
// upload.go).
type searchItem struct {
	Filename  string `json:"filename"`
	Category  string `json:"category"`
	ViewCount int64  `json:"viewCount"`
	URL       string `json:"url"`
}

type searchResponse struct {
	Items []searchItem `json:"items"`
}

// searchDiscover hits the same public, unauthenticated Discover listing the
// website's own search bar uses (scope=discover) - no API token needed,
// nothing here is account-specific.
func searchDiscover(cfg config, query string, limit int) ([]searchItem, error) {
	u := fmt.Sprintf("%s/api/media-info?scope=discover&sort=new&category=all&limit=%d&q=%s",
		cfg.apiBase, limit, url.QueryEscape(query))

	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spools api returned %d", resp.StatusCode)
	}

	var out searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
