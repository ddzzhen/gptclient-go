package sentinel

type Config struct {
	BearerToken  string
	CookieString string
	Model        string
	DeviceID     string
	BuildHash    string
	BuildNumber  string
	UserAgent    string
	Language     string
	CSRFToken    string
	ImageDir     string
	TempMode     bool

	BrowserMgr      *BrowserManager
	UseBrowserProxy bool
	DataDir         string
}

type ThinkStep struct {
	Summary string
	Content string
}

type ChatResult struct {
	Text                        string
	RequestedModel              string
	UpstreamModel               string
	ThinkingText                string
	ThinkSteps                  []ThinkStep
	deltaChannel                string
	sawAnalysisChannel          bool
	assistantFinalText          string
	emittedBodyLen              int
	bodyStreamFromSSE           bool
	seenThoughtKeys             map[string]bool
	ConversationID              string
	LastAssistantMsgID          string
	ImageTaskID                 string
	ImageFileID                 string
	ImageFileIDs                []string
	ExpectGeneratedImages       bool
	ImagePath                   string
	DalleStarted                bool
	ArtifactSignals             []ArtifactSignal
	SandboxArtifacts            []SandboxArtifact
	PDFArtifacts                []PDFArtifact
	emittedArtifacts            map[string]bool
	lastImageAddedAt            int64
	lastImageGenActivityAt      int64
	imageSlots                  map[string]*GeneratedImageSlot
	imageAsyncTaskActive        bool
	imageAsyncTaskPending       int
	imageGenAsyncCompleteSeen   bool
	imageGenConvAsyncStatusDone bool
	imageGenConvStatusAt        int64
	imageGenTurnDone            bool
}

type SessionInfo struct {
	ConversationID  string
	ParentMessageID string
	Model           string
	TempMode        bool
	TurnCount       int
}

type StreamHandler func(delta string)

type LogFunc func(format string, args ...interface{})
