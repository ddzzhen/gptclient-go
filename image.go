package sentinel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PollAndDownloadImage 轮询对话详情并下载 DALL-E 生成的图片
func (c *Client) PollAndDownloadImage(conversationID string) (string, error) {
	c.logf("[image] 等待图片生成...")
	const maxAttempts = 30

	for i := 0; i < maxAttempts; i++ {
		if i < 5 {
			time.Sleep(2 * time.Second)
		} else {
			time.Sleep(5 * time.Second)
		}

		resp, err := c.httpClient.R().
			SetHeaders(map[string]string{
				"Accept":       "*/*",
				"Content-Type": "application/json",
				"x-openai-target-path":  fmt.Sprintf("/backend-api/conversation/%s", conversationID),
				"x-openai-target-route": "/backend-api/conversation/{conversationId}",
			}).
			Get(fmt.Sprintf("/backend-api/conversation/%s", conversationID))
		if err != nil || resp.StatusCode != 200 {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal(resp.Bytes(), &data); err != nil {
			continue
		}

		mapping, ok := data["mapping"].(map[string]interface{})
		if !ok {
			continue
		}

		fileIDs := make(map[string]bool)
		for _, nodeRaw := range mapping {
			node, ok := nodeRaw.(map[string]interface{})
			if !ok {
				continue
			}
			msg, ok := node["message"].(map[string]interface{})
			if !ok {
				continue
			}

			if content, ok := msg["content"].(map[string]interface{}); ok {
				if ct, _ := content["content_type"].(string); ct == "multimodal_text" {
					parts, _ := content["parts"].([]interface{})
					for _, part := range parts {
						if partMap, ok := part.(map[string]interface{}); ok {
							if ap, ok := partMap["asset_pointer"].(string); ok {
								if fid := extractFileID(ap); fid != "" {
									fileIDs[fid] = true
								}
							}
						}
					}
				}
			}

			if meta, ok := msg["metadata"].(map[string]interface{}); ok {
				refs, _ := meta["content_references"].([]interface{})
				for _, ref := range refs {
					if refMap, ok := ref.(map[string]interface{}); ok {
						if ap, ok := refMap["asset_pointer"].(string); ok {
							if fid := extractFileID(ap); fid != "" {
								fileIDs[fid] = true
							}
						}
					}
				}
			}
		}

		for fid := range fileIDs {
			fp, err := c.downloadImage(fid, conversationID)
			if err == nil && fp != "" {
				return fp, nil
			}
		}
	}

	c.logf("[image] 超时，未能获取图片")
	return "", fmt.Errorf("image download timeout")
}

// DownloadImageByFileID 直接用已知 file_id 下载图片（从 WebSocket asset_pointer 提取）
func (c *Client) DownloadImageByFileID(fileID, conversationID string) (string, error) {
	return c.downloadImage(fileID, conversationID)
}

// downloadImage 通过 file_id 下载图片并保存到本地
func (c *Client) downloadImage(fileID, conversationID string) (string, error) {
	if err := os.MkdirAll(c.imageDir, 0755); err != nil {
		return "", err
	}

	filename := fmt.Sprintf("dalle_%d.png", time.Now().UnixMilli())
	fpath := filepath.Join(c.imageDir, filename)

	apiPath := fmt.Sprintf("/backend-api/files/download/%s?conversation_id=%s&inline=false", fileID, conversationID)
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"Accept":       "*/*",
			"Content-Type": "application/json",
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/files/download/{fileId}",
		}).
		Get(apiPath)
	if err != nil || resp.StatusCode != 200 {
		return "", fmt.Errorf("download info failed: status=%d", resp.StatusCode)
	}

	var dr struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(resp.Bytes(), &dr); err != nil || dr.DownloadURL == "" {
		return "", fmt.Errorf("no download_url in response")
	}

	c.logf("[image] 找到图片: %s", fileID)

	imgResp, err := c.httpClient.R().Get(dr.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}

	imgData := imgResp.Bytes()
	if len(imgData) < 1000 {
		return "", fmt.Errorf("image too small: %d bytes", len(imgData))
	}

	if err := os.WriteFile(fpath, imgData, 0644); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}

	c.logf("[image] 已保存: %s (%d KB)", fpath, len(imgData)/1024)
	return fpath, nil
}
