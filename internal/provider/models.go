package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// listModels reads an OpenAI-style ({"data":[{"id"}]}) or Ollama-style
// ({"models":[{"name"}]}) model list.
func listModels(ctx context.Context, c *Client, url string, header http.Header) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = header
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch models: HTTP %d: %.300s", resp.StatusCode, Redact(strings.TrimSpace(string(data))))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("fetch models: unexpected reply: %.200s", string(data))
	}
	seen := map[string]bool{}
	models := []string{}
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	for _, m := range list.Data {
		add(m.ID)
	}
	for _, m := range list.Models {
		add(m.Name)
	}
	return models, nil
}
