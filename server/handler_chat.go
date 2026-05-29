package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	sentinel "sentinel-go"
)

// ChatHandler 持有依赖，负责 /v1/chat/completions 路由
type ChatHandler struct {
	cfg     *ServerConfig
	pool    *TokenPool
	session *SessionManager
}

// NewChatHandler 创建 ChatHandler
func NewChatHandler(cfg *ServerConfig, pool *TokenPool, session *SessionManager) *ChatHandler {
	return &ChatHandler{cfg: cfg, pool: pool, session: session}
}

// Handle 处理 POST /v1/chat/completions
func (h *ChatHandler) Handle(c *gin.Context) {
	var req ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: ErrorDetail{Message: "Invalid JSON body", Type: "invalid_request_error"},
		})
		return
	}

	if req.Model == "" {
		req.Model = h.cfg.DefaultModel
	}

	// 获取当前请求使用的 ChatGPT token（由鉴权中间件写入）
	token := extractChatGPTToken(c)

	// 提取最后一条 user 消息作为本轮输入
	userMsg, systemPrompt, b64Images := extractUserMessage(req.Messages)
	if userMsg == "" && len(b64Images) == 0 {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: ErrorDetail{Message: "No user message or images found in messages", Type: "invalid_request_error"},
		})
		return
	}

	// 获取或创建 session（有状态多轮对话）
	entry := h.session.GetOrCreate(req.ConversationID, token)

	// 如果有 system prompt 且是新对话（无 conversationID），拼接到用户消息前面
	inputMsg := userMsg
	if systemPrompt != "" && req.ConversationID == "" && entry.client.GetModel() != "" {
		inputMsg = "[System]: " + systemPrompt + "\n\n" + userMsg
	}

	// 处理文件上传（图片 + 文档 + 其他类型）
	var uploadedImages []sentinel.UploadedFile
	for _, b64 := range b64Images {
		// 解析 data URL：data:<mime>;base64,<data>  或  data:<mime>,<data>
		if !strings.HasPrefix(b64, "data:") {
			continue
		}
		commaIdx := strings.Index(b64, ",")
		if commaIdx < 0 {
			continue
		}
		header := b64[5:commaIdx]   // e.g. "application/pdf;base64" or "image/jpeg;base64"
		payload := b64[commaIdx+1:] // base64 encoded data

		var data []byte
		var err error
		if strings.Contains(header, ";base64") {
			data, err = base64.StdEncoding.DecodeString(payload)
		} else {
			// 非 base64 编码（少见），直接用字节
			data = []byte(payload)
		}
		if err != nil || len(data) == 0 {
			continue
		}

		// 从 header 提取文件名后缀用于命名
		mimeHint := strings.TrimSuffix(header, ";base64")
		fileName := guessFileName(mimeHint)

		uf, err := entry.client.UploadFile(c.Request.Context(), data, fileName)
		if err == nil && uf != nil {
			uploadedImages = append(uploadedImages, *uf)
		}
	}

	// 切换模型（如果请求指定了不同的模型）
	if req.Model != "" && req.Model != entry.client.GetModel() {
		entry.client.SetModel(req.Model)
	}

	forcePicV2 := strings.Contains(strings.ToLower(req.Model), "dall-e") ||
		strings.Contains(strings.ToLower(req.Model), "gpt-image")

	opts := sentinel.ChatOptions{
		Text:           inputMsg,
		Images:         uploadedImages,
		ForcePictureV2: forcePicV2,
		ImageAspect:    sizeToAspect(req.Size),
	}

	chatID := "chatcmpl-" + sentinel.GenerateUUID()
	createdAt := time.Now().Unix()

	if req.Stream {
		h.handleStream(c, entry, opts, req.ConversationID, chatID, req.Model, createdAt)
	} else {
		h.handleNonStream(c, entry, opts, req.ConversationID, chatID, req.Model, createdAt)
	}
}

// handleStream 流式响应
func (h *ChatHandler) handleStream(c *gin.Context, entry *sessionEntry, opts sentinel.ChatOptions, reqConvID, chatID, model string, created int64) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 第一个 chunk：role=assistant
	firstSent := false
	registeredConvID := ""

	w := c.Writer
	flusher, canFlush := w.(http.Flusher)

	writeChunk := func(chunk ChatCompletionChunk) {
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	}

	handler := func(delta string) {
		if !firstSent {
			// 第一个有内容的 chunk，先发 role
			roleChunk := ChatCompletionChunk{
				ID:      chatID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []ChunkChoice{{
					Index:        0,
					Delta:        Delta{Role: "assistant"},
					FinishReason: nil,
				}},
			}
			writeChunk(roleChunk)
			firstSent = true
		}

		contentChunk := ChatCompletionChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []ChunkChoice{{
				Index:        0,
				Delta:        Delta{Content: delta},
				FinishReason: nil,
			}},
		}
		writeChunk(contentChunk)
	}

	result, err := entry.client.ChatStream(opts, sentinel.StreamHandler(handler))

	if err != nil {
		// 打印详细错误，方便排查 token 问题
		tokenPreview := ""
		if t := entry.token; len(t) > 20 {
			tokenPreview = t[:10] + "..." + t[len(t)-8:]
		} else {
			tokenPreview = entry.token
		}
		fmt.Printf("[chat-err] token=%s error=%v\n", tokenPreview, err)
		errChunk := fmt.Sprintf("data: {\"error\":{\"message\":%q,\"type\":\"server_error\"}}\n\n", err.Error())
		_, _ = io.WriteString(w, errChunk)
		if canFlush {
			flusher.Flush()
		}
		return
	}

	// 注册 session
	if result.ConversationID != "" {
		registeredConvID = result.ConversationID
		h.session.Register(registeredConvID, entry)
	}

	// 思考步骤详细内容（textdocs API 获取，流结束后推送）
	if len(result.ThinkSteps) > 0 {
		var thinkContent strings.Builder
		thinkContent.WriteString("\x00THINK_DETAILS\x00")
		for i, step := range result.ThinkSteps {
			if i > 0 {
				thinkContent.WriteString("\x00STEP_SEP\x00")
			}
			thinkContent.WriteString(step.Summary)
			thinkContent.WriteString("\x1F")
			thinkContent.WriteString(step.Content)
		}
		writeChunk(ChatCompletionChunk{
			ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []ChunkChoice{{Index: 0, Delta: Delta{Content: thinkContent.String()}, FinishReason: nil}},
		})
	}

	// 多图：为每个 file ID 生成代理 URL 并输出 markdown
	if len(result.ImageFileIDs) > 0 {
		var imgContent strings.Builder
		for i, fileID := range result.ImageFileIDs {
			proxyURL := fmt.Sprintf("/api/image/proxy?conv_id=%s&file_id=%s", registeredConvID, fileID)
			imgContent.WriteString(fmt.Sprintf("\n\n![Generated Image %d](%s)", i+1, proxyURL))
		}
		imgChunk := ChatCompletionChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []ChunkChoice{{
				Index:        0,
				Delta:        Delta{Content: imgContent.String()},
				FinishReason: nil,
			}},
		}
		writeChunk(imgChunk)
	} else if result.ImageFileID != "" {
		// 兼容旧单图逻辑
		proxyURL := fmt.Sprintf("/api/image/proxy?conv_id=%s&file_id=%s", registeredConvID, result.ImageFileID)
		imgChunk := ChatCompletionChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []ChunkChoice{{
				Index:        0,
				Delta:        Delta{Content: fmt.Sprintf("\n\n![Generated Image](%s)", proxyURL)},
				FinishReason: nil,
			}},
		}
		writeChunk(imgChunk)
	} else if result.ImagePath != "" {
		p := result.ImagePath
		if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
			p = strings.ReplaceAll(p, "\\", "/")
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
		}
		imgChunk := ChatCompletionChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []ChunkChoice{{
				Index:        0,
				Delta:        Delta{Content: fmt.Sprintf("\n\n![Generated Image](%s)", p)},
				FinishReason: nil,
			}},
		}
		writeChunk(imgChunk)
	}

	// 多 PDF：输出下载链接
	if len(result.PDFArtifacts) > 0 {
		var pdfContent strings.Builder
		for i, pdf := range result.PDFArtifacts {
			proxyURL := fmt.Sprintf("/api/pdf/proxy?conv_id=%s&msg_id=%s&sandbox_path=%s",
				registeredConvID, pdf.MessageID, url.QueryEscape(pdf.SandboxPath))
			label := pdf.FileName
			if label == "" {
				label = fmt.Sprintf("document_%d.pdf", i+1)
			}
			pdfContent.WriteString(fmt.Sprintf("\n\n[%s](%s)", label, proxyURL))
		}
		writeChunk(ChatCompletionChunk{
			ID: chatID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []ChunkChoice{{Index: 0, Delta: Delta{Content: pdfContent.String()}, FinishReason: nil}},
		})
	}

	// 最后一个 chunk（stop）
	stopReason := "stop"
	stopChunk := ChatCompletionChunk{
		ID:      chatID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []ChunkChoice{{
			Index:        0,
			Delta:        Delta{},
			FinishReason: &stopReason,
		}},
		ConversationID: registeredConvID,
	}
	writeChunk(stopChunk)

	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// handleNonStream 非流式响应
func (h *ChatHandler) handleNonStream(c *gin.Context, entry *sessionEntry, opts sentinel.ChatOptions, reqConvID, chatID, model string, created int64) {
	result, err := entry.client.Chat(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: ErrorDetail{Message: err.Error(), Type: "server_error"},
		})
		return
	}

	// 注册 session
	if result.ConversationID != "" {
		h.session.Register(result.ConversationID, entry)
	}

	content := result.Text

	// 多图：为每个 file ID 生成代理 URL
	if len(result.ImageFileIDs) > 0 {
		for i, fileID := range result.ImageFileIDs {
			proxyURL := fmt.Sprintf("/api/image/proxy?conv_id=%s&file_id=%s", result.ConversationID, fileID)
			content += fmt.Sprintf("\n\n![Generated Image %d](%s)", i+1, proxyURL)
		}
	} else if result.ImageFileID != "" {
		// 兼容旧单图逻辑
		proxyURL := fmt.Sprintf("/api/image/proxy?conv_id=%s&file_id=%s", result.ConversationID, result.ImageFileID)
		content += fmt.Sprintf("\n\n![Generated Image](%s)", proxyURL)
	} else if result.ImagePath != "" {
		p := result.ImagePath
		if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
			p = strings.ReplaceAll(p, "\\", "/")
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
		}
		content += fmt.Sprintf("\n\n![Generated Image](%s)", p)
	}

	// 多 PDF
	for i, pdf := range result.PDFArtifacts {
		proxyURL := fmt.Sprintf("/api/pdf/proxy?conv_id=%s&msg_id=%s&sandbox_path=%s",
			result.ConversationID, pdf.MessageID, url.QueryEscape(pdf.SandboxPath))
		label := pdf.FileName
		if label == "" {
			label = fmt.Sprintf("document_%d.pdf", i+1)
		}
		content += fmt.Sprintf("\n\n[%s](%s)", label, proxyURL)
	}

	resp := ChatCompletionResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage:          Usage{},
		ConversationID: result.ConversationID,
	}
	c.JSON(http.StatusOK, resp)
}

// parseMessageContent 解析多模态内容或纯文本内容
func parseMessageContent(c interface{}) (text string, images []string) {
	if c == nil {
		return
	}
	if s, ok := c.(string); ok {
		return s, nil
	}
	if arr, ok := c.([]interface{}); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				t, _ := m["type"].(string)
				if t == "text" {
					if txt, ok := m["text"].(string); ok {
						text += txt
					}
				} else if t == "image_url" {
					if imgUrl, ok := m["image_url"].(map[string]interface{}); ok {
						if url, ok := imgUrl["url"].(string); ok {
							images = append(images, url)
						}
					}
				}
			}
		}
	}
	return
}

// extractUserMessage 从 messages 中提取最后一条 user 消息和 system 提示词
func extractUserMessage(messages []Message) (userMsg string, systemPrompt string, images []string) {
	// 找 system prompt
	for _, m := range messages {
		if strings.ToLower(m.Role) == "system" {
			systemPrompt, _ = parseMessageContent(m.Content)
			break
		}
	}
	// 找最后一条 user 消息
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.ToLower(messages[i].Role) == "user" {
			userMsg, images = parseMessageContent(messages[i].Content)
			break
		}
	}
	return
}

// HandleImageProxy 处理图片流式代理请求
func (h *ChatHandler) HandleImageProxy(c *gin.Context) {
	convID := c.Query("conv_id")
	fileID := c.Query("file_id")
	if convID == "" || fileID == "" {
		c.String(http.StatusBadRequest, "Missing conv_id or file_id")
		return
	}

	entry, ok := h.session.GetSession(convID)
	if !ok {
		c.String(http.StatusNotFound, "Session not found or expired")
		return
	}

	userAgent := c.GetHeader("User-Agent")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}

	err := entry.client.ProxyImageByFileID(fileID, convID, c.Writer, userAgent)
	if err != nil {
		c.String(http.StatusInternalServerError, "Proxy image failed: %v", err)
	}
}

// HandlePDFProxy 代理下载 Code Interpreter 生成的 PDF
func (h *ChatHandler) HandlePDFProxy(c *gin.Context) {
	convID := c.Query("conv_id")
	msgID := c.Query("msg_id")
	sandboxPath := c.Query("sandbox_path")
	if convID == "" || msgID == "" || sandboxPath == "" {
		c.String(http.StatusBadRequest, "Missing conv_id, msg_id or sandbox_path")
		return
	}

	entry, ok := h.session.GetSession(convID)
	if !ok {
		c.String(http.StatusNotFound, "Session not found or expired")
		return
	}

	userAgent := c.GetHeader("User-Agent")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}

	if err := entry.client.ProxyPDFBySandboxPath(convID, msgID, sandboxPath, c.Writer, userAgent); err != nil {
		c.String(http.StatusInternalServerError, "Proxy PDF failed: %v", err)
	}
}

// guessFileName 根据 MIME 类型猜测一个合适的文件名
func guessFileName(mime string) string {
	extMap := map[string]string{
		"image/jpeg":                                                          "upload.jpg",
		"image/png":                                                           "upload.png",
		"image/gif":                                                           "upload.gif",
		"image/webp":                                                          "upload.webp",
		"application/pdf":                                                     "document.pdf",
		"application/msword":                                                  "document.doc",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "document.docx",
		"application/vnd.ms-excel":                                           "spreadsheet.xls",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":  "spreadsheet.xlsx",
		"application/vnd.ms-powerpoint":                                      "presentation.ppt",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "presentation.pptx",
		"text/plain":                                                          "document.txt",
		"text/csv":                                                            "data.csv",
		"application/json":                                                    "data.json",
		"text/markdown":                                                       "document.md",
	}
	if name, ok := extMap[mime]; ok {
		return name
	}
	return "file"
}

// sizeToAspect 将 OpenAI 风格的 size 字符串转换为 ImageAspectRatio。
// 支持 "1:1" / "3:4" / "9:16" / "4:3" / "16:9" 宽高比直写，
// 以及兼容 OpenAI 像素格式 "256x256" / "1024x1024" / "1792x1024" / "1024x1792"。
func sizeToAspect(size string) sentinel.ImageAspectRatio {
	switch strings.TrimSpace(strings.ToLower(size)) {
	case "1:1", "256x256", "512x512", "1024x1024":
		return sentinel.ImageAspectSquare
	case "3:4":
		return sentinel.ImageAspectPortrait
	case "9:16", "1024x1792":
		return sentinel.ImageAspectStory
	case "4:3":
		return sentinel.ImageAspectLandscape
	case "16:9", "1792x1024":
		return sentinel.ImageAspectWidescreen
	default:
		return sentinel.ImageAspectAuto
	}
}
