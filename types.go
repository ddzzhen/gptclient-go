package sentinel

// Config 客户端配置
type Config struct {
	BearerToken  string // 必需：ChatGPT Bearer Token (JWT)
	CookieString string // 可选：Cookie 字符串
	Model        string // 可选：默认 "gpt-5-5-thinking"
	DeviceID     string // 可选：设备 ID，留空自动生成 UUID
	BuildHash    string // 可选：客户端构建哈希
	BuildNumber  string // 可选：客户端构建号
	UserAgent    string // 可选：User-Agent 字符串
	Language     string // 可选：语言，默认 "zh-CN"
	ImageDir     string // 可选：图片保存目录，默认 "images"
	TempMode     bool   // 可选：临时模式（不保存对话历史）
}

// ThinkStep 思考过程中的一个步骤（默认由 SSE thoughts 填充；可选 fetchTextdocs）
type ThinkStep struct {
	Summary string // 步骤标题（简短）
	Content string // 详细推理内容
}

// ChatResult 单轮对话结果
type ChatResult struct {
	Text               string        // 助手回复的完整文本
	ThinkingText       string        // 思考过程文本（analysis channel，用于追踪增量）
	ThinkSteps         []ThinkStep   // 思考步骤列表（含 summary + content 详细内容）
	deltaChannel       string        // 内部：当前 delta 消息的 channel（analysis / final / ""）
	sawAnalysisChannel bool          // 本 turn 是否出现过 analysis（用于未标 channel 的 patch 分流）
	assistantFinalText string        // final channel 正文（优先作为 result.Text）
	emittedBodyLen     int           // 已通过 handler 下发的正文字节数（防 WS catchup 重放）
	bodyStreamFromSSE  bool          // HTTP SSE 已输出过正文，WS catchups 应跳过
	seenThoughtKeys    map[string]bool // 内部：已推送过的 thought key（summary，去重用）
	ConversationID     string        // 对话 ID
	LastAssistantMsgID string        // 最后一条助手消息 ID（用于多轮衔接）
	ImageTaskID        string        // DALL-E 图片任务触发标志（如有）
	ImageFileID        string        // 首张图片文件 ID（兼容旧逻辑，等同于 ImageFileIDs[0]）
	ImageFileIDs           []string // 所有生成图片的文件 ID 列表（多图场景）
	ExpectGeneratedImages  bool     // 本轮为 DALL·E/picture_v2 生图（非上传文件识图里的 sediment 引用）
	ImagePath              string   // 已下载图片本地路径（如有）
	DalleStarted       bool              // 标记是否已输出正在画图的提示
	ArtifactSignals    []ArtifactSignal  // 流式/对话中观测到的产物信号（用于通用轮询判断）
	SandboxArtifacts   []SandboxArtifact // Code Interpreter 沙箱产物（pdf/txt/等）
	PDFArtifacts       []PDFArtifact     // 兼容：.pdf 子集，与 SandboxArtifacts 同步填充
	emittedArtifacts   map[string]bool              // 已推送的产物键（防重复）
	lastImageAddedAt   int64                        // 纳秒时间戳，多图 idle 结束用（仅 DALL·E 产出更新）
	imageSlots         map[string]*GeneratedImageSlot // gen_id/message_id → 图位
	imageAsyncTaskActive bool                         // 已收到 async-task-start，WS 应等待 conversation-update
}

// SessionInfo 当前会话状态快照
type SessionInfo struct {
	ConversationID  string
	ParentMessageID string
	Model           string
	TempMode        bool
	TurnCount       int
}

// StreamHandler 流式回调，每次收到文本增量时被调用
type StreamHandler func(delta string)

// LogFunc 日志输出函数签名
type LogFunc func(format string, args ...interface{})
