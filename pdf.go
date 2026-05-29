package sentinel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

// PDFArtifact 表示 Code Interpreter 生成的 PDF 文件
type PDFArtifact struct {
	MessageID   string `json:"message_id"`
	SandboxPath string `json:"sandbox_path"`
	FileName    string `json:"file_name"`
}

var sandboxPDFRe = regexp.MustCompile(`/mnt/data/[^\s"'*?]+\.pdf`)

// pollPDFStreamStatus 等待对话流结束后，从 conversation mapping 提取所有 PDF 沙箱路径
func (c *Client) pollPDFStreamStatus(conversationID string) ([]PDFArtifact, string, error) {
	const (
		totalTimeout = 10 * time.Minute
		pollInterval = 5 * time.Second
	)
	deadline := time.Now().Add(totalTimeout)

	// 等待 SSE stream 结束
	for time.Now().Before(deadline) {
		status, err := c.fetchStreamStatus(conversationID)
		if err != nil {
			c.logf("[pdf-poll] stream_status 请求失败: %v，重试...", err)
		} else if strings.EqualFold(status, "COMPLETE") {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// 轮询对话直到出现 PDF 路径
	for time.Now().Before(deadline) {
		pdfs, lastMsgID, err := c.fetchConversationPDFArtifacts(conversationID)
		if err != nil {
			c.logf("[pdf-poll] 对话查询失败: %v，重试...", err)
			time.Sleep(pollInterval)
			continue
		}
		if len(pdfs) > 0 {
			return pdfs, lastMsgID, nil
		}
		c.logf("[pdf-poll] PDF 尚未就绪，等待中...")
		time.Sleep(pollInterval)
	}
	return nil, "", fmt.Errorf("等待 PDF 超时（%v）", totalTimeout)
}

// fetchConversationPDFArtifacts 从对话 mapping 中提取所有 PDF 沙箱文件
func (c *Client) fetchConversationPDFArtifacts(conversationID string) ([]PDFArtifact, string, error) {
	apiPath := "/backend-api/conversation/" + conversationID
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/conversation/{conversation_id}",
		}).
		Get(apiPath)
	if err != nil {
		return nil, "", fmt.Errorf("获取对话失败: %w", err)
	}

	var conv map[string]interface{}
	if err := json.Unmarshal(resp.Bytes(), &conv); err != nil {
		return nil, "", fmt.Errorf("解析对话失败: %w", err)
	}

	currentNode, _ := conv["current_node"].(string)
	mapping, _ := conv["mapping"].(map[string]interface{})

	// 收集所有 sandbox PDF 路径及关联 message_id
	type pathInfo struct {
		path      string
		messageID string
	}
	seen := make(map[string]bool)
	var paths []pathInfo

	for nodeID, nodeRaw := range mapping {
		node, ok := nodeRaw.(map[string]interface{})
		if !ok {
			continue
		}
		msg, ok := node["message"].(map[string]interface{})
		if !ok {
			continue
		}
		msgID, _ := msg["id"].(string)
		if msgID == "" {
			msgID = nodeID
		}

		// 从消息 JSON 中递归搜索 /mnt/data/*.pdf
		found := extractSandboxPDFPaths(msg)
		for _, p := range found {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, pathInfo{path: p, messageID: msgID})
			}
		}
	}

	if len(paths) == 0 {
		return nil, currentNode, fmt.Errorf("对话中未找到 PDF 文件")
	}

	// 优先使用包含 content_references 的 assistant 消息 ID（与网页端一致）
	ownerMsgID := findPDFOwnerMessageID(mapping)
	if ownerMsgID == "" {
		ownerMsgID = paths[0].messageID
	}

	var artifacts []PDFArtifact
	for _, pi := range paths {
		artifacts = append(artifacts, PDFArtifact{
			MessageID:   ownerMsgID,
			SandboxPath: pi.path,
			FileName:    path.Base(pi.path),
		})
	}
	return artifacts, currentNode, nil
}

// findPDFOwnerMessageID 找到包含 content_references 的 assistant 消息（PDF 列表所在消息）
func findPDFOwnerMessageID(mapping map[string]interface{}) string {
	for _, nodeRaw := range mapping {
		node, ok := nodeRaw.(map[string]interface{})
		if !ok {
			continue
		}
		msg, ok := node["message"].(map[string]interface{})
		if !ok {
			continue
		}
		author, _ := msg["author"].(map[string]interface{})
		if role, _ := author["role"].(string); role != "assistant" {
			continue
		}
		meta, _ := msg["metadata"].(map[string]interface{})
		if refs, ok := meta["content_references"].([]interface{}); ok && len(refs) > 0 {
			for _, ref := range refs {
				refMap, _ := ref.(map[string]interface{})
				if refMap == nil {
					continue
				}
				refType, _ := refMap["type"].(string)
				if strings.Contains(refType, "file") || strings.Contains(refType, "sandbox") ||
					refMap["sandbox_path"] != nil {
					if id, _ := msg["id"].(string); id != "" {
						return id
					}
				}
			}
			// 有 content_references 的 assistant 消息
			if id, _ := msg["id"].(string); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractSandboxPDFPaths(v interface{}) []string {
	var out []string
	switch x := v.(type) {
	case string:
		for _, m := range sandboxPDFRe.FindAllString(x, -1) {
			out = append(out, m)
		}
	case map[string]interface{}:
		for _, val := range x {
			out = append(out, extractSandboxPDFPaths(val)...)
		}
	case []interface{}:
		for _, item := range x {
			out = append(out, extractSandboxPDFPaths(item)...)
		}
	}
	return out
}

// resolvePDFDownloadURL 调用 interpreter/download 获取 PDF 下载直链
func (c *Client) resolvePDFDownloadURL(conversationID, messageID, sandboxPath string) (string, error) {
	apiPath := fmt.Sprintf("/backend-api/conversation/%s/interpreter/download", conversationID)
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/conversation/{conversation_id}/interpreter/download",
		}).
		SetQueryParams(map[string]string{
			"message_id":   messageID,
			"sandbox_path": sandboxPath,
		}).
		Get(apiPath)
	if err != nil {
		return "", err
	}
	if resp.IsErrorState() {
		return "", fmt.Errorf("interpreter/download failed: status=%d body=%s", resp.StatusCode, resp.String())
	}
	var result struct {
		Status      string `json:"status"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		return "", err
	}
	if result.DownloadURL == "" {
		return "", fmt.Errorf("empty download_url")
	}
	return result.DownloadURL, nil
}

// ProxyPDFBySandboxPath 代理下载 PDF 并写入 ResponseWriter
func (c *Client) ProxyPDFBySandboxPath(conversationID, messageID, sandboxPath string, w interface{}, reqUserAgent string) error {
	writer, ok := w.(http.ResponseWriter)
	if !ok {
		return fmt.Errorf("invalid http.ResponseWriter")
	}

	downloadURL, err := c.resolvePDFDownloadURL(conversationID, messageID, sandboxPath)
	if err != nil {
		return err
	}
	c.logf("[pdf] 下载直链: %s", downloadURL)

	reqHeader := map[string]string{
		"User-Agent": reqUserAgent,
		"Accept":     "application/pdf,*/*",
	}
	var fileResp *req.Response
	if strings.Contains(downloadURL, "chatgpt.com") {
		fileResp, err = c.httpClient.R().SetHeaders(reqHeader).Get(downloadURL)
	} else {
		cleanClient := req.C().ImpersonateChrome()
		fileResp, err = cleanClient.R().SetHeaders(reqHeader).Get(downloadURL)
	}
	if err != nil {
		return fmt.Errorf("proxy fetch pdf failed: %w", err)
	}
	if fileResp.IsErrorState() {
		return fmt.Errorf("proxy fetch pdf error: status=%d", fileResp.StatusCode)
	}

	data := fileResp.Bytes()
	contentType := fileResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/pdf"
	}
	filename := path.Base(sandboxPath)
	if filename == "" || filename == "." {
		filename = "document.pdf"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	_, err = writer.Write(data)
	return err
}

func pdfNames(pdfs []PDFArtifact) []string {
	names := make([]string, len(pdfs))
	for i, p := range pdfs {
		names[i] = p.FileName
	}
	return names
}

// shouldPollPDF 判断是否需要轮询 PDF 生成结果
func shouldPollPDF(userPrompt, assistantText string) bool {
	lower := strings.ToLower(userPrompt + " " + assistantText)
	return strings.Contains(lower, "pdf") ||
		strings.Contains(userPrompt, "PDF") ||
		strings.Contains(lower, "生成") && strings.Contains(lower, "文档")
}

// encodeSandboxPathForQuery URL 编码 sandbox_path（保留 / 由 query 参数处理）
func encodeSandboxPathForQuery(sandboxPath string) string {
	return url.QueryEscape(sandboxPath)
}
