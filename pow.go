package sentinel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/sha3"
)

type TurnstileSolver interface {
	Solve(ctx context.Context, dx string) (string, error)
}

const (
	powPrefixRequirements = "gAAAAAC"
	powPrefixProof        = "gAAAAAB"

	requirementsDifficulty = "0fffff"

	maxRequirementsIter = 500_000
	maxProofIter        = 100_000

	powFallback = "gAAAAABwQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"
)

var (
	powCores   = []int{2, 4, 6, 8, 10, 12, 16, 20, 24, 32, 48, 64}
	powScreens = []int{1366, 1440, 1536, 1600, 1680, 1920, 2560, 3840}

	powNavKeys = []string{
		"webdriver-false", "vendor-Google Inc.", "cookieEnabled-true",
		"pdfViewerEnabled-true", "hardwareConcurrency-8",
		"language-zh-CN", "mimeTypes-[object MimeTypeArray]",
		"userAgentData-[object NavigatorUAData]",
		"webdriver-false", "vendor-Google Inc.", "cookieEnabled-true",
		"pdfViewerEnabled-true", "hardwareConcurrency-12",
		"language-en-US", "mimeTypes-[object MimeTypeArray]",
		"userAgentData-[object NavigatorUAData]",
		"webdriver-false", "vendor-Google Inc.", "cookieEnabled-true",
		"pdfViewerEnabled-true", "hardwareConcurrency-16",
		"language-zh-CN", "mimeTypes-[object MimeTypeArray]",
		"userAgentData-[object NavigatorUAData]",
		"webdriver-false", "vendor-Google Inc.", "cookieEnabled-true",
		"pdfViewerEnabled-true", "hardwareConcurrency-4",
		"language-en-US", "mimeTypes-[object MimeTypeArray]",
		"userAgentData-[object NavigatorUAData]",
	}
	powWinKeys = []string{
		"innerWidth", "innerHeight", "devicePixelRatio", "screen",
		"chrome", "location", "history", "navigator",
		"outerWidth", "outerHeight", "screenX", "screenY",
		"pageXOffset", "pageYOffset", "visualViewport",
	}

	powReactListeners = []string{
		"_reactListeningcfilawjnerp", "_reactListening9ne2dfo1i47",
		"_reactListening8xk3mnp2oq1", "_reactListening5ty7uvw9rs0",
		"_reactListening2ab4cde6fg3", "_reactListening1hi5jkl7mn9",
	}
	powProofEvents = []string{
		"alert", "ontransitionend", "onprogress",
		"onanimationend", "onload", "onerror",
		"onfocus", "onblur", "onresize",
	}

	perfCounter uint64
)

type POWConfig struct {
	userAgent string
	arr       [18]interface{}
}

func NewPOWConfig(userAgent string) *POWConfig {
	if userAgent == "" {
		userAgent = defaultUA
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	now := time.Now().UTC()
	timeStr := now.Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (UTC)"
	perf := float64(atomic.AddUint64(&perfCounter, 1)) + rng.Float64()

	cores := powCores[rng.Intn(len(powCores))]
	screenW := powScreens[rng.Intn(len(powScreens))]

	navKey := fmt.Sprintf("webdriver-false-vendor-Google Inc.-cookieEnabled-true-pdfViewerEnabled-true-hardwareConcurrency-%d-language-%s-mimeTypes-[object MimeTypeArray]-userAgentData-[object NavigatorUAData]",
		cores,
		pickLang(rng))

	dpl := "dpl=1440a687921de39ff5ee56b92807faaadce73f13"
	if currentDPL != "" {
		dpl = currentDPL
	}

	c := &POWConfig{userAgent: userAgent}
	c.arr = [18]interface{}{
		cores + screenW,
		timeStr,
		nil,
		rng.Float64(),
		userAgent,
		nil,
		dpl,
		"zh-CN",
		"zh-CN,zh,en-US,en",
		0,
		navKey,
		"location",
		powWinKeys[rng.Intn(len(powWinKeys))],
		perf,
		randomUUID(rng),
		"",
		rng.Intn(8) + 4,
		now.Unix(),
	}
	return c
}

func (c *POWConfig) RequirementsToken() string {
	seed := strconv.FormatFloat(rand.Float64(), 'f', -1, 64)
	b64, ok := c.solveRequirements(seed, requirementsDifficulty)
	if !ok {
		return powPrefixRequirements + powFallback +
			base64.StdEncoding.EncodeToString([]byte(`"`+seed+`"`))
	}
	return powPrefixRequirements + b64
}

func (c *POWConfig) solveRequirements(seed, difficulty string) (string, bool) {
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", false
	}
	diffLen := len(difficulty)

	arr := c.arr
	head, _ := json.Marshal([]interface{}{arr[0], arr[1], arr[2]})
	p1 := append(head[:len(head)-1:len(head)-1], ',')

	mid, _ := json.Marshal([]interface{}{arr[4], arr[5], arr[6], arr[7], arr[8]})
	p2 := make([]byte, 0, len(mid)+2)
	p2 = append(p2, ',')
	p2 = append(p2, mid[1:len(mid)-1]...)
	p2 = append(p2, ',')

	tail, _ := json.Marshal([]interface{}{
		arr[10], arr[11], arr[12], arr[13], arr[14], arr[15], arr[16], arr[17],
	})
	p3 := make([]byte, 0, len(tail)+1)
	p3 = append(p3, ',')
	p3 = append(p3, tail[1:]...)

	hasher := sha3.New512()
	seedB := []byte(seed)
	buf := make([]byte, 0, len(p1)+32+len(p2)+16+len(p3))
	b64buf := make([]byte, base64.StdEncoding.EncodedLen(cap(buf)))

	for i := 0; i < maxRequirementsIter; i++ {
		d1 := strconv.Itoa(i)
		d2 := strconv.Itoa(i >> 1)

		buf = buf[:0]
		buf = append(buf, p1...)
		buf = append(buf, d1...)
		buf = append(buf, p2...)
		buf = append(buf, d2...)
		buf = append(buf, p3...)

		n := base64.StdEncoding.EncodedLen(len(buf))
		if cap(b64buf) < n {
			b64buf = make([]byte, n)
		}
		b64buf = b64buf[:n]
		base64.StdEncoding.Encode(b64buf, buf)

		hasher.Reset()
		hasher.Write(seedB)
		hasher.Write(b64buf)
		sum := hasher.Sum(nil)

		n2 := diffLen
		if n2 > len(sum) {
			n2 = len(sum)
		}
		cmpLen := n2
		if cmpLen > len(target) {
			cmpLen = len(target)
		}
		if bytes.Compare(sum[:cmpLen], target[:cmpLen]) <= 0 {
			return string(b64buf), true
		}
	}
	return "", false
}

func SolveProofToken(seed, difficulty, userAgent string) (string, bool) {
	if seed == "" || difficulty == "" {
		return "", false
	}
	if userAgent == "" {
		userAgent = defaultUA
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	screen := powScreens[rng.Intn(len(powScreens))] * (1 << rng.Intn(3))

	timeStr := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")

	turnstileURL := "https://tcr9i.chat.openai.com/v2/35536E1E-65B4-4D96-9D97-6ADB7EFF8147/api.js"
	if currentTurnstileURL != "" {
		turnstileURL = currentTurnstileURL
	}

	dpl := "dpl=1440a687921de39ff5ee56b92807faaadce73f13"
	if currentDPL != "" {
		dpl = currentDPL
	}

	proofConfig := []interface{}{
		screen,
		timeStr,
		nil,
		0,
		userAgent,
		turnstileURL,
		dpl,
		"zh-CN",
		"zh-CN",
		nil,
		fmt.Sprintf("plugins-[object PluginArray]"),
		powReactListeners[rng.Intn(len(powReactListeners))],
		powProofEvents[rng.Intn(len(powProofEvents))],
	}

	diffLen := len(difficulty)
	hasher := sha3.New512()
	for i := 0; i < maxProofIter; i++ {
		proofConfig[3] = i
		raw, err := json.Marshal(proofConfig)
		if err != nil {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(raw)
		hasher.Reset()
		hasher.Write([]byte(seed + b64))
		sum := hasher.Sum(nil)
		hexStr := hex.EncodeToString(sum)
		if strings.Compare(hexStr[:diffLen], difficulty) <= 0 {
			return powPrefixProof + b64, true
		}
	}
	return powPrefixProof + powFallback +
		base64.StdEncoding.EncodeToString([]byte(`"`+seed+`"`)), false
}

var (
	currentDPL         string
	currentTurnstileURL string
)

func SetDPL(dpl string) {
	currentDPL = dpl
}

func SetTurnstileURL(url string) {
	currentTurnstileURL = url
}

func pickLang(rng *rand.Rand) string {
	langs := []string{"zh-CN", "en-US", "zh-TW", "ja-JP", "ko-KR"}
	return langs[rng.Intn(len(langs))]
}

func randomUUID(rng *rand.Rand) string {
	var b [16]byte
	_, _ = rng.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
