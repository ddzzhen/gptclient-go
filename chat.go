package sentinel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ImageAspectRatio 图片宽高比
type ImageAspectRatio string

const (
	ImageAspectAuto      ImageAspectRatio = ""      // 自动（默认）
	ImageAspectSquare    ImageAspectRatio = "1:1"   // 方形
	ImageAspectPortrait  ImageAspectRatio = "3:4"   // 竖版
	ImageAspectStory     ImageAspectRatio = "9:16"  // 故事版
	ImageAspectLandscape ImageAspectRatio = "4:3"   // 横版
	ImageAspectWidescreen ImageAspectRatio = "16:9" // 宽屏
)

// ChatOptions 对话请求参数
type ChatOptions struct {
	Text           string
	Images         []UploadedFile
	ForcePictureV2 bool
	// ImageAspect 仅在 ForcePictureV2=true 时生效，指定生成图片的宽高比
	ImageAspect ImageAspectRatio
}

// Chat 发送一轮对话，返回完整结果（非流式）
func (c *Client) Chat(opts ChatOptions) (*ChatResult, error) {
	return c.ChatStream(opts, nil)
}

// ChatStream 发送一轮对话，通过 handler 回调实时接收增量文本
func (c *Client) ChatStream(opts ChatOptions, handler StreamHandler) (*ChatResult, error) {
	turnTraceID := GenerateUUID()

	c.logf("[step 1] 获取 conduit token...")
	conduitToken, err := c.getConduitToken(c.model, turnTraceID, runeSlice(opts.Text, 5))
	if err != nil {
		return nil, fmt.Errorf("get conduit token: %w", err)
	}

	c.logf("[step 2] 获取 sentinel token...")
	sentinelToken, proofToken, err := c.getSentinelToken()
	if err != nil {
		return nil, fmt.Errorf("get sentinel token: %w", err)
	}

	c.logf("[step 2.5] 建立 WebSocket 连接...")
	wsConn, err := c.dialChatWS()
	if err != nil {
		return nil, fmt.Errorf("dial ws: %w", err)
	}
	defer wsConn.Close()

	promptText := opts.Text
	if opts.ForcePictureV2 && opts.ImageAspect != ImageAspectAuto {
		promptText += "\n\n将宽高比设为 " + string(opts.ImageAspect)
	}

	// 区分图片（multimodal）和文档（my_files）
	// 图片需要插入 content.parts 作为 image_asset_pointer；文档只放 metadata.attachments
	var parts []interface{}
	hasImages := false
	for _, f := range opts.Images {
		if f.UseCase == "multimodal" {
			parts = append(parts, f.ToAssetPointerPart())
			hasImages = true
		}
	}
	parts = append(parts, promptText)

	contentType := "text"
	if hasImages {
		contentType = "multimodal_text"
	}

	attachments := []Attachment{}
	for _, f := range opts.Images {
		attachments = append(attachments, f.ToAttachment())
	}

	msgID := GenerateUUID()
	userMsgObj := map[string]interface{}{
		"id":          msgID,
		"author":      map[string]string{"role": "user"},
		"create_time": float64(time.Now().UnixMilli()) / 1000.0,
		"content": map[string]interface{}{
			"content_type": contentType,
			"parts":        parts,
		},
		"metadata": map[string]interface{}{
			"developer_mode_connector_ids": []string{},
			"selected_sources":             []string{},
			"selected_github_repos":        []string{},
			"selected_all_github_repos":    false,
			"serialization_metadata":       map[string]interface{}{"custom_symbol_offsets": []interface{}{}},
		},
	}
	if len(attachments) > 0 {
		userMsgObj["metadata"].(map[string]interface{})["attachments"] = attachments
	}

	systemHints := []string{}
	if opts.ForcePictureV2 {
		systemHints = append(systemHints, "picture_v2")
		meta := userMsgObj["metadata"].(map[string]interface{})
		meta["system_hints"] = systemHints
		// picture_v2 不能带 selected_sources，否则直接失败 (静默失败)
		delete(meta, "selected_sources")
	}

	body := map[string]interface{}{
		"action": "next",
		"messages": []map[string]interface{}{
			userMsgObj,
		},
		"parent_message_id":        c.parentMessageID,
		"model":                    c.model,
		"timezone_offset_min":      -480,
		"timezone":                 "Asia/Shanghai",
		"conversation_mode":        map[string]string{"kind": "primary_assistant"},
		"enable_message_followups": true,
		"system_hints":             systemHints,
		"supports_buffering":       true,
		"supported_encodings":      []string{"v1"},
		"client_contextual_info": map[string]interface{}{
			"is_dark_mode":      false,
			"time_since_loaded": int(math.Round(perfNowMs(c.startTime) / 1000.0)),
			"page_height":       1014,
			"page_width":        1055,
			"pixel_ratio":       1,
			"screen_height":     1080,
			"screen_width":      1920,
			"app_name":          "chatgpt.com",
		},
		"history_and_training_disabled":        c.tempMode,
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
		"thinking_effort":                      "standard",
	}
	if c.conversationID != "" {
		body["conversation_id"] = c.conversationID
	}

	convDesc := c.conversationID
	if convDesc == "" {
		convDesc = "(新对话)"
	}
	c.logf("[step 3] 发送对话: model=%s, conversation=%s, turn=%d", c.model, convDesc, c.turnCount+1)

	result, err := c.streamConversation(body, sentinelToken, proofToken, conduitToken, turnTraceID, wsConn, handler)
	if err != nil {
		return nil, err
	}

	if result.ConversationID != "" {
		c.conversationID = result.ConversationID
	}
	if result.LastAssistantMsgID != "" {
		c.parentMessageID = result.LastAssistantMsgID
	}
	c.turnCount++

	c.logf("[info] conversation_id=%s, turn=%d, reply=%d字",
		c.conversationID, c.turnCount, len([]rune(result.Text)))

	if !c.DisableAutoImage && (result.ImageFileID == "" && result.DalleStarted) {
		if fid, err := c.PollForImageFileID(result.ConversationID); err == nil {
			result.ImageFileID = fid
		}
	}

	// PDF 生成场景：轮询 conversation 中的 sandbox PDF 路径
	if shouldPollPDF(opts.Text, result.Text) && result.ConversationID != "" && len(result.ImageFileIDs) == 0 {
		c.logf("[pdf-poll] 开始轮询 PDF 生成进度, conversation=%s", result.ConversationID)
		if pdfs, lastMsgID, err := c.pollPDFStreamStatus(result.ConversationID); err != nil {
			c.logf("[pdf-poll] 轮询失败: %v", err)
		} else {
			result.PDFArtifacts = pdfs
			if lastMsgID != "" {
				result.LastAssistantMsgID = lastMsgID
				c.parentMessageID = lastMsgID
			}
			c.logf("[pdf-poll] PDF 已就绪: %d 个, %v", len(pdfs), pdfNames(pdfs))
		}
	}
	if len(result.PDFArtifacts) > 0 {
		result.Text = ""
	}

	// 思考模型：获取思考步骤详细内容（textdocs API）
	if result.ConversationID != "" {
		c.logf("[textdocs] 获取思考步骤详细内容, conversation=%s", result.ConversationID)
		if steps, err := c.fetchTextdocs(result.ConversationID); err != nil {
			c.logf("[textdocs] 获取失败（非致命）: %v", err)
		} else if len(steps) > 0 {
			result.ThinkSteps = steps
			c.logf("[textdocs] 获取到 %d 个思考步骤", len(steps))
		}
	}

	return result, nil
}

// getWsURL 调用 celsius/ws/user 获取 WebSocket 连接地址
func (c *Client) getWsURL() (string, error) {
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"Accept":                "*/*",
			"x-openai-target-path":  "/backend-api/celsius/ws/user",
			"x-openai-target-route": "/backend-api/celsius/ws/user",
		}).
		Get("/backend-api/celsius/ws/user")
	if err != nil {
		return "", fmt.Errorf("celsius/ws/user request: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("celsius/ws/user %d: %s", resp.StatusCode, truncateStr(resp.String(), 200))
	}
	var result struct {
		WebsocketURL string `json:"websocket_url"`
	}
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		return "", fmt.Errorf("parse celsius/ws/user: %w", err)
	}
	if result.WebsocketURL == "" {
		return "", fmt.Errorf("empty websocket_url")
	}
	return result.WebsocketURL, nil
}

// dialChatWS 获取 ws url 并完成握手+初始化订阅，返回已就绪的连接
func (c *Client) dialChatWS() (*websocket.Conn, error) {
	wsURL, err := c.getWsURL()
	if err != nil {
		return nil, err
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	hdrs := http.Header{}
	hdrs.Set("User-Agent", c.userAgent)
	hdrs.Set("Origin", "https://chatgpt.com")

	conn, _, err := dialer.Dial(wsURL, hdrs)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}

	// 初始化：connect + 订阅三个基础 topic
	initMsg := []map[string]interface{}{
		{"id": 1, "command": map[string]interface{}{
			"type":     "connect",
			"presence": map[string]string{"type": "presence", "state": "background"},
		}},
		{"id": 2, "command": map[string]interface{}{"type": "subscribe", "topic_id": "calpico-chatgpt"}},
		{"id": 3, "command": map[string]interface{}{"type": "subscribe", "topic_id": "conversations"}},
		{"id": 4, "command": map[string]interface{}{"type": "subscribe", "topic_id": "app_notifications"}},
	}
	if err := conn.WriteJSON(initMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws init send: %w", err)
	}

	// 不等待初始化 reply，由 subscribeWSStream 的读取循环统一处理所有帧
	return conn, nil
}

// wsIDCounter 用于 WebSocket 命令 id 自增（跨调用）
var wsIDCounter int64 = 4

func nextWsID() int64 {
	return atomic.AddInt64(&wsIDCounter, 1)
}

// streamConversation 发 f/conversation，解析 stream_handoff 后走 WebSocket 续流
func (c *Client) streamConversation(body interface{}, sentinelToken, proofToken, conduitToken, turnTraceID string, wsConn *websocket.Conn, handler StreamHandler) (*ChatResult, error) {
	headers := map[string]string{
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
		"openai-sentinel-chat-requirements-token": sentinelToken,
		"x-conduit-token":                         conduitToken,
		"x-oai-turn-trace-id":                     turnTraceID,
		"x-openai-target-path":                    "/backend-api/f/conversation",
		"x-openai-target-route":                   "/backend-api/f/conversation",
	}
	if proofToken != "" {
		headers["openai-sentinel-proof-token"] = proofToken
	}

	resp, err := c.httpClient.R().
		SetHeaders(headers).
		SetBody(body).
		DisableAutoReadResponse().
		Post("/backend-api/f/conversation")
	if err != nil {
		return nil, fmt.Errorf("conversation request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("conversation %d: %s", resp.StatusCode, truncateStr(string(b), 500))
	}

	result := &ChatResult{}
	var lastText string
	var useDeltaEncoding bool
	var currentEvent string
	var handoffTopicID string

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimSpace(line[7:])
			if currentEvent == "delta_encoding" {
				useDeltaEncoding = true
			}
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimSpace(line[6:])
		if payload == "" || payload == "[DONE]" || payload == `"v1"` {
			continue
		}

		var evt map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			currentEvent = ""
			continue
		}

		if strings.Contains(payload, "dalle") || strings.Contains(payload, `"tool"`) || strings.Contains(payload, "image") || strings.Contains(payload, "thought") || strings.Contains(payload, "reasoning_content") {
			c.logf("[debug-sse] payload: %s", payload)
		}

		if cid, ok := evt["conversation_id"].(string); ok && cid != "" {
			result.ConversationID = cid
		}

		evtType, _ := evt["type"].(string)
		switch evtType {
		case "resume_conversation_token":
			currentEvent = ""
			continue
		case "stream_handoff":
			_, topicID := parseStreamHandoff(evt)
			if topicID != "" {
				handoffTopicID = topicID
			}
			currentEvent = ""
			continue
		}

		// server_ste_metadata 事件（生图/思考场景）：提取 turn_exchange_id（备用）
		if currentEvent == "server_ste_metadata" {
			if tid, ok := evt["turn_exchange_id"].(string); ok && tid != "" && handoffTopicID == "" {
				handoffTopicID = "conversation-turn-" + tid
			}
		}

		checkImageTaskID(evt, result)
		if useDeltaEncoding && currentEvent == "delta" {
			c.processDeltaSSE(evt, result, &lastText, handler)
		} else {
			c.processFullSSE(evt, result, &lastText, handler)
		}
		currentEvent = ""
	}

	// 图片生成场景：即使已有文本（如"正在处理图片"提示），也必须继续等待 WebSocket 图片
	if !c.DisableAutoImage && result.ImageTaskID != "" && result.ConversationID != "" {
		// 图片生成场景：使用 HTTP 轮询 stream_status，比 WebSocket 更可靠
		c.logf("[image-poll] 开始轮询图片生成进度, conversation=%s", result.ConversationID)
		if fileIDs, lastMsgID, err := c.pollImageStreamStatus(result.ConversationID); err != nil {
			c.logf("[image-poll] 轮询失败: %v", err)
		} else {
			result.ImageFileIDs = fileIDs
			if len(fileIDs) > 0 {
				result.ImageFileID = fileIDs[0]
			}
			// 用图片生成后对话链的最后节点 ID 作为下轮 parent_message_id，确保多轮对话连续
			if lastMsgID != "" {
				result.LastAssistantMsgID = lastMsgID
				c.logf("[image-poll] 更新 parentMsgID=%s", lastMsgID)
			}
			c.logf("[image-poll] 图片已就绪: %d 张, IDs=%v", len(fileIDs), fileIDs)
		}
	} else if handoffTopicID != "" && wsConn != nil {
		// 普通文字场景走 topic SSE 续流
		c.logf("[handoff] 订阅 WebSocket topic: %s", handoffTopicID)
		if err := c.subscribeWSStream(wsConn, handoffTopicID, result, &lastText, handler); err != nil {
			return nil, fmt.Errorf("ws stream: %w", err)
		}
	}

	// 图片生成成功后清除排队提示文字，只保留图片 URL
	if len(result.ImageFileIDs) > 0 {
		lastText = ""
	}
	result.Text = lastText
	return result, nil
}

// parseWSFrames 将 WebSocket 文本帧解析为帧列表（支持 JSON 数组或单对象）
func parseWSFrames(raw []byte) []map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		var frames []map[string]interface{}
		if err := json.Unmarshal(raw, &frames); err != nil {
			return nil
		}
		return frames
	}
	var single map[string]interface{}
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil
	}
	return []map[string]interface{}{single}
}

// processConvUpdatePayload 处理 conversation-update 的 payload：输出 analysis 文字，若发现图片则写入 result.ImageFileID 并返回 true
func (c *Client) processConvUpdatePayload(payload map[string]interface{}, result *ChatResult, handler StreamHandler) bool {
	updateContent, ok := payload["update_content"].(map[string]interface{})
	if !ok {
		return false
	}
	messages, ok := updateContent["messages"].([]interface{})
	if !ok {
		return false
	}

	for _, msgI := range messages {
		msg, ok := msgI.(map[string]interface{})
		if !ok {
			continue
		}
		author, _ := msg["author"].(map[string]interface{})
		role, _ := author["role"].(string)
		channel, _ := msg["channel"].(string)
		msgContent, _ := msg["content"].(map[string]interface{})
		parts, _ := msgContent["parts"].([]interface{})

		if channel == "analysis" {
			for _, part := range parts {
				if text, ok := part.(string); ok && text != "" {
					if handler != nil {
						handler(text)
					}
				}
			}
			continue
		}

		if role == "tool" {
			name, _ := author["name"].(string)
			status, _ := msg["status"].(string)

			isImageTool := strings.Contains(name, "dalle") || strings.Contains(name, "image_gen")

			if isImageTool && status == "in_progress" {
				if handler != nil && !result.DalleStarted {
					prompt := ""
					for _, p := range parts {
						if pStr, ok := p.(string); ok && pStr != "" {
							prompt += pStr
						}
					}
					if prompt != "" {
						handler(fmt.Sprintf("\n\n[正在生成图片: %s...]\n\n", prompt))
					} else {
						handler("\n\n[正在生成图片，请稍候...]\n\n")
					}
					result.DalleStarted = true
				}
			}

			for _, part := range parts {
				partMap, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				if partMap["content_type"] == "image_asset_pointer" {
					assetPtr, _ := partMap["asset_pointer"].(string)
					if fileID := strings.TrimPrefix(assetPtr, "sediment://"); fileID != assetPtr && fileID != "" {
						c.logf("[image-ws] 收到图片 asset_pointer: %s", fileID)
						result.ImageFileID = fileID
						return true
					}
				}
			}
		}
	}
	return false
}

// subscribeWSImageCombined 生图：订阅 conversation-turn-* 消费流式 delta，同时处理 conversation-update 拿图片
func (c *Client) subscribeWSImageCombined(conn *websocket.Conn, turnTopicID, conversationID string, result *ChatResult, lastText *string, handler StreamHandler) error {
	subID := nextWsID()
	subMsg := []map[string]interface{}{
		{"id": subID, "command": map[string]interface{}{
			"type":     "subscribe",
			"topic_id": turnTopicID,
			"offset":   "0",
		}},
	}
	if err := conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("ws subscribe send: %w", err)
	}

	var useDeltaEncoding bool
	var currentEvent string

	const totalTimeout = 10 * time.Minute
	const pingInterval = 25 * time.Second
	const readDeadlineExt = 60 * time.Second
	deadline := time.Now().Add(totalTimeout)

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readDeadlineExt))
		return nil
	})
	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			case <-stopPing:
				return
			}
		}
	}()
	defer close(stopPing)

	conn.SetReadDeadline(time.Now().Add(readDeadlineExt))
	defer conn.SetReadDeadline(time.Time{})

	for result.ImageFileID == "" {
		if time.Now().After(deadline) {
			return fmt.Errorf("超过最大等待时间 %.0f 分钟，图片未返回", totalTimeout.Minutes())
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(readDeadlineExt))

		frames := parseWSFrames(raw)
		for _, frame := range frames {
			fType, _ := frame["type"].(string)
			switch fType {
			case "conversation-update":
				payload, ok := frame["payload"].(map[string]interface{})
				if !ok {
					continue
				}
				if cid, _ := payload["conversation_id"].(string); cid != conversationID {
					continue
				}
				if c.processConvUpdatePayload(payload, result, handler) {
					return nil
				}
			case "reply":
				reply, ok := frame["reply"].(map[string]interface{})
				if !ok {
					continue
				}
				replyTopicID, _ := reply["topic_id"].(string)
				if replyTopicID != turnTopicID {
					continue
				}
				catchups, _ := reply["catchups"].([]interface{})
				c.logf("[ws] reply catchups=%d", len(catchups))
				for _, cu := range catchups {
					if msg, ok := cu.(map[string]interface{}); ok {
						_ = c.processWSMessage(msg, result, lastText, handler, &useDeltaEncoding, &currentEvent)
					}
				}
			case "message":
				frameTopic, _ := frame["topic_id"].(string)
				if frameTopic != turnTopicID {
					continue
				}
				_ = c.processWSMessage(frame, result, lastText, handler, &useDeltaEncoding, &currentEvent)
			}
		}
	}
	return nil
}

// subscribeWSStream 通过已有 WebSocket 连接订阅 topic 并消费 encoded_item 里的 SSE 数据
func (c *Client) subscribeWSStream(conn *websocket.Conn, topicID string, result *ChatResult, lastText *string, handler StreamHandler) error {
	subID := nextWsID()
	subMsg := []map[string]interface{}{
		{"id": subID, "command": map[string]interface{}{
			"type":     "subscribe",
			"topic_id": topicID,
			"offset":   "0",
		}},
	}
	if err := conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("ws subscribe send: %w", err)
	}

	var useDeltaEncoding bool
	var currentEvent string
	done := false

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	for !done {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))

		frames := parseWSFrames(raw)
		if len(frames) == 0 {
			continue
		}

		for _, frame := range frames {
			fType, _ := frame["type"].(string)

			if fType == "reply" {
				reply, ok := frame["reply"].(map[string]interface{})
				if !ok {
					continue
				}
				replyTopicID, _ := reply["topic_id"].(string)
				if replyTopicID != topicID {
					continue
				}
				catchups, _ := reply["catchups"].([]interface{})
				c.logf("[ws] reply catchups=%d", len(catchups))
				for _, cu := range catchups {
					if msg, ok := cu.(map[string]interface{}); ok {
						d := c.processWSMessage(msg, result, lastText, handler, &useDeltaEncoding, &currentEvent)
						if d {
							done = true
						}
					}
				}
				continue
			}

			if fType == "message" {
				frameTopic, _ := frame["topic_id"].(string)
				if frameTopic != topicID {
					continue
				}
				d := c.processWSMessage(frame, result, lastText, handler, &useDeltaEncoding, &currentEvent)
				if d {
					done = true
				}
			}
		}
	}

	return nil
}

// subscribeWSConvUpdate 监听 WebSocket 的 conversation-update 消息（生图场景，无 turn topic 时）
// 通过定期 Ping 心跳保活连接，最长等待 10 分钟。
func (c *Client) subscribeWSConvUpdate(conn *websocket.Conn, conversationID string, result *ChatResult, handler StreamHandler) error {
	const totalTimeout = 10 * time.Minute
	const pingInterval = 25 * time.Second
	const readDeadlineExt = 60 * time.Second

	deadline := time.Now().Add(totalTimeout)

	// Pong handler：收到服务端 pong 后重置读 deadline
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readDeadlineExt))
		return nil
	})

	// 心跳 goroutine：每 25s 发一次 Ping
	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			case <-stopPing:
				return
			}
		}
	}()
	defer close(stopPing)

	conn.SetReadDeadline(time.Now().Add(readDeadlineExt))
	defer conn.SetReadDeadline(time.Time{})

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("超过最大等待时间 %.0f 分钟，图片未返回", totalTimeout.Minutes())
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(readDeadlineExt))

		for _, frame := range parseWSFrames(raw) {
			if fType, _ := frame["type"].(string); fType != "conversation-update" {
				continue
			}
			payload, ok := frame["payload"].(map[string]interface{})
			if !ok {
				continue
			}
			if cid, _ := payload["conversation_id"].(string); cid != conversationID {
				continue
			}
			if c.processConvUpdatePayload(payload, result, handler) {
				return nil
			}
		}
	}
}

// processWSMessage 处理单条 WebSocket message 帧，返回 true 表示流结束
func (c *Client) processWSMessage(frame map[string]interface{}, result *ChatResult, lastText *string, handler StreamHandler, useDeltaEncoding *bool, currentEvent *string) bool {
	payload1, ok := frame["payload"].(map[string]interface{})
	if !ok {
		return false
	}
	payload2, ok := payload1["payload"].(map[string]interface{})
	if !ok {
		return false
	}
	encoded, ok := payload2["encoded_item"].(string)
	if !ok || encoded == "" {
		return false
	}

	// encoded_item 是 SSE 格式文本，逐行解析
	for _, line := range strings.Split(encoded, "\n") {
		line = strings.TrimRight(line, "\r")

		if strings.HasPrefix(line, "event: ") {
			*currentEvent = strings.TrimSpace(line[7:])
			if *currentEvent == "delta_encoding" {
				*useDeltaEncoding = true
			}
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		ssePayload := strings.TrimSpace(line[6:])
		if ssePayload == "" || ssePayload == `"v1"` {
			continue
		}
		if ssePayload == "[DONE]" {
			return true
		}

		var evt map[string]interface{}
		if err := json.Unmarshal([]byte(ssePayload), &evt); err != nil {
			*currentEvent = ""
			continue
		}

		if cid, ok := evt["conversation_id"].(string); ok && cid != "" {
			result.ConversationID = cid
		}

		evtType, _ := evt["type"].(string)
		if evtType == "resume_conversation_token" || evtType == "stream_handoff" {
			*currentEvent = ""
			continue
		}

		checkImageTaskID(evt, result)
		if *useDeltaEncoding && *currentEvent == "delta" {
			c.processDeltaSSE(evt, result, lastText, handler)
		} else {
			c.processFullSSE(evt, result, lastText, handler)
		}
		*currentEvent = ""
	}
	return false
}

// parseStreamHandoff 从 stream_handoff 事件中提取 resume_sse_endpoint 的 topic_id
func parseStreamHandoff(evt map[string]interface{}) (bool, string) {
	options, ok := evt["options"].([]interface{})
	if !ok {
		return false, ""
	}
	for _, optRaw := range options {
		opt, ok := optRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if typ, _ := opt["type"].(string); typ == "subscribe_ws_topic" {
			topicID, _ := opt["topic_id"].(string)
			return topicID != "", topicID
		}
	}
	return false, ""
}

// checkImageTaskID 从 SSE 事件中提取图片任务 ID（兼容旧版 image_gen_task_id 和新版 ghostrider）
func checkImageTaskID(evt map[string]interface{}, result *ChatResult) {
	extractFromMeta := func(meta map[string]interface{}) {
		if tid, ok := meta["image_gen_task_id"].(string); ok && tid != "" {
			result.ImageTaskID = tid
			return
		}
		if result.ImageTaskID == "" {
			if _, ok := meta["ghostrider"]; ok {
				result.ImageTaskID = "ghostrider"
			}
		}
	}

	if v, ok := evt["v"].(map[string]interface{}); ok {
		if msg, ok := v["message"].(map[string]interface{}); ok {
			if meta, ok := msg["metadata"].(map[string]interface{}); ok {
				extractFromMeta(meta)
			}
		}
	}
}

// processDeltaSSE 处理 delta 编码模式的 SSE 事件
// ChatGPT delta 格式有多种变体：
//  A) 顶层 patch：{"p":"/message/content/parts/0","o":"append","v":"text"}
//  B) 简写 append：{"v":"text"}（省略 p/o，隐含对 parts/0 的追加）
//  C) 消息对象 add：{"p":"","o":"add","v":{"message":{...}}}
//  D) 完成 patch 数组：{"p":"","o":"patch","v":[...patches...]}
func (c *Client) processDeltaSSE(evt map[string]interface{}, result *ChatResult, lastText *string, handler StreamHandler) {
	pPath, _ := evt["p"].(string)
	pOp, _ := evt["o"].(string)

	// 格式 A：顶层 append patch
	if pPath == "/message/content/parts/0" && pOp == "append" {
		if text, ok := evt["v"].(string); ok && text != "" {
			// 判断当前 channel 是否是思考（由上层 add 初始化时记录）
			if result.deltaChannel == "analysis" {
				result.ThinkingText += text
				if handler != nil {
					handler("\x00THINK\x00" + text)
				}
			} else {
				*lastText += text
				if handler != nil {
					handler(text)
				}
			}
		}
		return
	}

	v := evt["v"]

	// 格式 B：只有 v 字段，且是字符串 → 隐含 append
	_, hasP := evt["p"]
	_, hasO := evt["o"]
	if !hasP && !hasO {
		if text, ok := v.(string); ok && text != "" {
			if result.deltaChannel == "analysis" {
				result.ThinkingText += text
				if handler != nil {
					handler("\x00THINK\x00" + text)
				}
			} else {
				*lastText += text
				if handler != nil {
					handler(text)
				}
			}
			return
		}
	}

	// 格式 C：v 是包含 message 的 map（消息对象初始化或 final channel）
	if vMap, ok := v.(map[string]interface{}); ok {
		if msgRaw, exists := vMap["message"]; exists {
			if msg, ok := msgRaw.(map[string]interface{}); ok {
				author := getNestedString(msg, "author", "role")
				channel, _ := msg["channel"].(string)
				msgID, _ := msg["id"].(string)

				if author == "assistant" && msgID != "" {
					result.LastAssistantMsgID = msgID
					// 记录当前消息的 channel，供后续 append patch 使用
					result.deltaChannel = channel

					// content_type="thoughts"：解析思考步骤（summary + content）
					if content, ok := msg["content"].(map[string]interface{}); ok {
						if ct, _ := content["content_type"].(string); ct == "thoughts" {
							if thoughts, ok := content["thoughts"].([]interface{}); ok {
								c.extractThoughts(thoughts, result, handler)
							}
						}
					}
				}
				if author == "tool" {
					if meta, ok := msg["metadata"].(map[string]interface{}); ok {
						if tid, ok := meta["image_gen_task_id"].(string); ok && tid != "" {
							result.ImageTaskID = tid
						}
						// 新版 ghostrider 异步生图：没有 image_gen_task_id，用 "ghostrider" 作为触发标志
						if result.ImageTaskID == "" {
							if _, ok := meta["ghostrider"]; ok {
								result.ImageTaskID = "ghostrider"
							}
						}
						// 思考模型：reasoning_title 是每步工具调用的思考标题
						if title, ok := meta["reasoning_title"].(string); ok && title != "" {
							// 同时取 content.parts[0] 作为执行输出
							execOutput := ""
							if content, ok := msg["content"].(map[string]interface{}); ok {
								if text, ok := content["text"].(string); ok {
									execOutput = text
								} else if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
									if s, ok := parts[0].(string); ok {
										execOutput = s
									}
								}
							}
							if handler != nil {
								payload := title
								if execOutput != "" {
									payload += "\x1F" + execOutput // \x1F 单元分隔符
								}
								handler("\x00THINK_STEP\x00" + payload)
							}
						}
					}
				}
				// final channel 上的完整文本（通常是最后确认，此时 lastText 应已累积完整）
				if author == "assistant" && channel == "final" {
					if text := getFirstStringPart(msg); text != "" && len(text) > len(*lastText) {
						delta := text[len(*lastText):]
						*lastText = text
						if handler != nil && delta != "" {
							handler(delta)
						}
					}
				}
			}
		}
	}

	// 格式 D：v 是 patches 数组（批量 patch）
	if patches, ok := v.([]interface{}); ok {
		for _, p := range patches {
			if patch, ok := p.(map[string]interface{}); ok {
				pp, _ := patch["p"].(string)
				po, _ := patch["o"].(string)
				if pp == "/message/content/parts/0" && po == "append" {
					if text, ok := patch["v"].(string); ok && text != "" {
						if result.deltaChannel == "analysis" {
							result.ThinkingText += text
							if handler != nil {
								handler("\x00THINK\x00" + text)
							}
						} else {
							*lastText += text
							if handler != nil {
								handler(text)
							}
						}
					}
				}
			}
		}
	}
}

// processFullSSE 处理非 delta 编码模式的 SSE 事件
func (c *Client) processFullSSE(evt map[string]interface{}, result *ChatResult, lastText *string, handler StreamHandler) {
	msgRaw, exists := evt["message"]
	if !exists {
		return
	}
	msg, ok := msgRaw.(map[string]interface{})
	if !ok {
		return
	}

	author := getNestedString(msg, "author", "role")
	channel, _ := msg["channel"].(string)
	msgID, _ := msg["id"].(string)

	if author == "assistant" && msgID != "" {
		result.LastAssistantMsgID = msgID

		// content_type="thoughts"：解析思考步骤（summary + content）
		if content, ok := msg["content"].(map[string]interface{}); ok {
			if ct, _ := content["content_type"].(string); ct == "thoughts" {
				if thoughts, ok := content["thoughts"].([]interface{}); ok {
					c.extractThoughts(thoughts, result, handler)
				}
			}
		}
	}

	if meta, ok := msg["metadata"].(map[string]interface{}); ok {
		if tid, ok := meta["image_gen_task_id"].(string); ok && tid != "" {
			result.ImageTaskID = tid
		}
		// 思考模型：tool 消息中的 reasoning_title 是每步思考标题
		if author == "tool" {
			if title, ok := meta["reasoning_title"].(string); ok && title != "" {
				execOutput := ""
				if content, ok := msg["content"].(map[string]interface{}); ok {
					if text, ok := content["text"].(string); ok {
						execOutput = text
					} else if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
						if s, ok := parts[0].(string); ok {
							execOutput = s
						}
					}
				}
				if handler != nil {
					payload := title
					if execOutput != "" {
						payload += "\x1F" + execOutput
					}
					handler("\x00THINK_STEP\x00" + payload)
				}
			}
		}
	}

	if author == "assistant" {
		text := getFirstStringPart(msg)
		if text == "" {
			return
		}
		if channel == "analysis" {
			// 思考过程：增量发送，用 lastThinkText 追踪已发量
			if len(text) > len(result.ThinkingText) {
				delta := text[len(result.ThinkingText):]
				result.ThinkingText = text
				if handler != nil {
					handler("\x00THINK\x00" + delta)
				}
			}
		} else {
			// 正文（含 final）
			if len(text) > len(*lastText) {
				delta := text[len(*lastText):]
				if handler != nil {
					handler(delta)
				}
				*lastText = text
			}
		}
	}
}

// pollImageStreamStatus 通过 HTTP 轮询等待图片生成完成，返回所有图片 file ID 列表和对话末尾消息 ID
// 策略：先等 stream_status=COMPLETE，再持续轮询对话，直到没有 intermediate 节点为止
// （多图场景：每张图片是独立节点，分批完成，必须等所有节点完成才能拿全）
// 返回：fileIDs, lastMsgID（对话 current_node，用于下轮 parent_message_id）, error
func (c *Client) pollImageStreamStatus(conversationID string) ([]string, string, error) {
	const (
		totalTimeout = 10 * time.Minute
		pollInterval = 5 * time.Second
	)
	deadline := time.Now().Add(totalTimeout)

	// 第一阶段：等待 SSE stream 结束（通常几秒内）
	for time.Now().Before(deadline) {
		status, err := c.fetchStreamStatus(conversationID)
		if err != nil {
			c.logf("[image-poll] stream_status 请求失败: %v，重试...", err)
		} else {
			c.logf("[image-poll] stream_status=%s", status)
			if strings.EqualFold(status, "COMPLETE") {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	// 第二阶段：轮询对话详情
	// 多图时每张图片是独立节点，必须等所有节点都不再 intermediate 才算完成
	for time.Now().Before(deadline) {
		fileIDs, lastMsgID, hasPending, err := c.fetchConversationImageFileIDs(conversationID)
		if err != nil {
			c.logf("[image-poll] 对话查询失败: %v，重试...", err)
			time.Sleep(pollInterval)
			continue
		}
		if hasPending {
			c.logf("[image-poll] 已收到 %d 张图片，仍有生成中的节点，继续等待...", len(fileIDs))
			time.Sleep(pollInterval)
			continue
		}
		if len(fileIDs) > 0 {
			return fileIDs, lastMsgID, nil
		}
		c.logf("[image-poll] 图片尚未就绪，等待中...")
		time.Sleep(pollInterval)
	}
	return nil, "", fmt.Errorf("等待图片超时（%v）", totalTimeout)
}

// fetchStreamStatus 查询对话的 stream_status
func (c *Client) fetchStreamStatus(conversationID string) (string, error) {
	apiPath := "/backend-api/conversation/" + conversationID + "/stream_status"
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/conversation/{conversation_id}/stream_status",
		}).
		Get(apiPath)
	if err != nil {
		return "", err
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		return "", err
	}
	return result.Status, nil
}

// fetchConversationImageFileIDs 获取对话中所有图片文件 ID 列表
// 返回：fileIDs（已就绪图片 ID），currentNode（对话末尾节点 ID，用于下轮 parent_message_id），hasPending，error
// 多图场景下，每张图片可能是独立的 multimodal_text 节点，分批完成
func (c *Client) fetchConversationImageFileIDs(conversationID string) (fileIDs []string, currentNode string, hasPending bool, err error) {
	apiPath := "/backend-api/conversation/" + conversationID
	resp, respErr := c.httpClient.R().
		SetHeaders(map[string]string{
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/conversation/{conversation_id}",
		}).
		Get(apiPath)
	if respErr != nil {
		return nil, "", false, fmt.Errorf("获取对话失败: %w", respErr)
	}

	var conv map[string]interface{}
	if jsonErr := json.Unmarshal(resp.Bytes(), &conv); jsonErr != nil {
		return nil, "", false, fmt.Errorf("解析对话失败: %w", jsonErr)
	}

	// current_node 是对话链末尾节点 ID，用于下轮对话的 parent_message_id
	currentNode, _ = conv["current_node"].(string)

	mapping, _ := conv["mapping"].(map[string]interface{})
	seen := make(map[string]bool) // 去重：同一 file ID 可能出现在多个节点中

	for _, nodeRaw := range mapping {
		node, ok := nodeRaw.(map[string]interface{})
		if !ok {
			continue
		}
		msg, ok := node["message"].(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := msg["content"].(map[string]interface{})
		if ct, _ := content["content_type"].(string); ct != "multimodal_text" {
			continue
		}

		// 检查该节点是否仍在生成中
		isIntermediate := false
		if meta, ok := msg["metadata"].(map[string]interface{}); ok {
			if gr, ok := meta["ghostrider"].(map[string]interface{}); ok {
				if grStatus, _ := gr["status"].(string); grStatus == "intermediate" {
					isIntermediate = true
				}
			}
			// is_temporal_turn=true 也表示异步还未完成
			if isTemporal, _ := meta["is_temporal_turn"].(bool); isTemporal {
				isIntermediate = true
			}
		}

		if isIntermediate {
			hasPending = true
		}

		// 收集所有 image_asset_pointer，去重处理
		parts, _ := content["parts"].([]interface{})
		for _, p := range parts {
			part, _ := p.(map[string]interface{})
			if pct, _ := part["content_type"].(string); pct == "image_asset_pointer" {
				ptr, _ := part["asset_pointer"].(string)
				if strings.HasPrefix(ptr, "sediment://") {
					fileID := strings.TrimPrefix(ptr, "sediment://")
					if !seen[fileID] {
						seen[fileID] = true
						fileIDs = append(fileIDs, fileID)
					}
				}
			}
		}
	}

	return fileIDs, currentNode, hasPending, nil
}

// fetchConversationImageFileID 兼容旧调用，返回第一张图片的 file ID
func (c *Client) fetchConversationImageFileID(conversationID string) (string, error) {
	ids, _, _, err := c.fetchConversationImageFileIDs(conversationID)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("对话中未找到图片文件 ID")
	}
	return ids[0], nil
}

// fetchTextdocs 调用 textdocs API 获取思考步骤的详细内容
// textdocs 返回一个对象数组，每个对象包含 type、thought（含 summary/content）等字段
func (c *Client) fetchTextdocs(conversationID string) ([]ThinkStep, error) {
	apiPath := "/backend-api/conversation/" + conversationID + "/textdocs"
	resp, err := c.httpClient.R().
		SetHeaders(map[string]string{
			"x-openai-target-path":  apiPath,
			"x-openai-target-route": "/backend-api/conversation/{conversation_id}/textdocs",
		}).
		Get(apiPath)
	if err != nil {
		return nil, fmt.Errorf("textdocs 请求失败: %w", err)
	}
	if resp.IsErrorState() {
		return nil, fmt.Errorf("textdocs 返回错误: status=%d body=%s", resp.StatusCode, resp.String()[:min(200, len(resp.String()))])
	}

	// textdocs 返回格式：{"textdocs": [{"type": 0, "thought": {"summary": "...", "content": "...", ...}}, ...]}
	// 或直接是数组
	rawBody := resp.String()
	c.logf("[textdocs] 原始响应 status=%d len=%d snippet=%s", resp.StatusCode, len(rawBody), rawBody[:min(500, len(rawBody))])

	var rawData interface{}
	if err := json.Unmarshal(resp.Bytes(), &rawData); err != nil {
		return nil, fmt.Errorf("textdocs 解析失败: %w", err)
	}

	var chunks []interface{}
	switch v := rawData.(type) {
	case map[string]interface{}:
		// 可能是 {"textdocs": [...]} 或 {"chunks": [...]}
		for _, key := range []string{"textdocs", "chunks", "items", "data"} {
			if arr, ok := v[key].([]interface{}); ok {
				chunks = arr
				break
			}
		}
		if chunks == nil {
			c.logf("[textdocs] 未知顶层结构, keys=%v", mapKeys(v))
		}
	case []interface{}:
		chunks = v
	}

	var steps []ThinkStep
	for _, chunkRaw := range chunks {
		chunk, ok := chunkRaw.(map[string]interface{})
		if !ok {
			continue
		}
		// type=0 是思考段落
		chunkType, _ := chunk["type"].(float64)
		if int(chunkType) != 0 {
			continue
		}
		thought, ok := chunk["thought"].(map[string]interface{})
		if !ok {
			continue
		}
		summary, _ := thought["summary"].(string)
		content, _ := thought["content"].(string)
		if summary == "" && content == "" {
			continue
		}
		steps = append(steps, ThinkStep{
			Summary: summary,
			Content: content,
		})
	}
	return steps, nil
}

// extractThoughts 从 content_type="thoughts" 消息的 thoughts 数组中提取已完成的思考步骤。
// SSE 流中的数组元素格式：{"summary": "...", "content": "...", "chunks": [...], "finished": true}
// 每个 finished=true 的步骤通过 \x00THINK_STEP\x00 标记推送一次（summary\x1Fcontent），去重处理。
func (c *Client) extractThoughts(thoughts []interface{}, result *ChatResult, handler StreamHandler) {
	if result.seenThoughtKeys == nil {
		result.seenThoughtKeys = make(map[string]bool)
	}
	for _, tRaw := range thoughts {
		t, ok := tRaw.(map[string]interface{})
		if !ok {
			continue
		}
		// SSE 格式：直接包含 summary, content, finished
		finished, _ := t["finished"].(bool)
		if !finished {
			continue
		}
		summary, _ := t["summary"].(string)
		content, _ := t["content"].(string)
		if summary == "" {
			continue
		}
		// 去重：同一个 summary 只推送一次
		if result.seenThoughtKeys[summary] {
			continue
		}
		result.seenThoughtKeys[summary] = true
		result.ThinkSteps = append(result.ThinkSteps, ThinkStep{Summary: summary, Content: content})
		c.logf("[thoughts] 新思考步骤: %s", summary)
		if handler != nil {
			payload := summary
			if content != "" {
				payload += "\x1F" + content
			}
			handler("\x00THINK_STEP\x00" + payload)
		}
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
