package sentinel

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AvailableModel 是 chatgpt.com 当前账号可选择的网页模型。
type AvailableModel struct {
	Slug  string
	Title string
}

// GetAvailableModels 从 chatgpt.com 动态获取当前账号可用的模型列表。
func (c *Client) GetAvailableModels() ([]AvailableModel, error) {
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"Accept":                "application/json",
			"x-openai-target-path":  "/backend-api/models",
			"x-openai-target-route": "/backend-api/models",
		}).
		SetQueryParam("history_and_training_disabled", "false").
		Get("/backend-api/models")
	if err != nil {
		return nil, fmt.Errorf("models request: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, ClassifyHTTPError("models", resp.StatusCode, resp.String(), resp.Header.Get("Retry-After"))
	}

	var payload struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(resp.Bytes(), &payload); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}
	if len(payload.Models) == 0 {
		return nil, fmt.Errorf("models response contains no models")
	}

	models := make([]AvailableModel, 0, len(payload.Models))
	seen := make(map[string]bool, len(payload.Models))
	for _, raw := range payload.Models {
		var item struct {
			Slug      string `json:"slug"`
			ID        string `json:"id"`
			Title     string `json:"title"`
			Name      string `json:"name"`
			ModelSlug string `json:"model_slug"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		slug := firstNonEmpty(item.Slug, item.ModelSlug, item.ID)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		models = append(models, AvailableModel{
			Slug:  slug,
			Title: firstNonEmpty(item.Title, item.Name, slug),
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models response contains no usable model slugs")
	}
	return models, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
