package sentinel



import (

	"encoding/json"

	"fmt"

	"net/http"

	"net/url"

	"strings"



	"github.com/imroc/req/v3"

)



// PDFArtifact 与 SandboxArtifact 同构（兼容旧字段名）。

type PDFArtifact = SandboxArtifact



func filterPDFArtifacts(arts []SandboxArtifact) []PDFArtifact {

	var out []PDFArtifact

	for _, a := range arts {

		if strings.HasSuffix(strings.ToLower(a.FileName), ".pdf") {

			out = append(out, PDFArtifact(a))

		}

	}

	return out

}



func sandboxNames(arts []SandboxArtifact) []string {

	names := make([]string, len(arts))

	for i, a := range arts {

		names[i] = a.FileName

	}

	return names

}



func pdfNames(pdfs []PDFArtifact) []string {

	return sandboxNames(artsFromPDF(pdfs))

}



func artsFromPDF(pdfs []PDFArtifact) []SandboxArtifact {

	out := make([]SandboxArtifact, len(pdfs))

	for i, p := range pdfs {

		out[i] = SandboxArtifact(p)

	}

	return out

}



// resolvePDFDownloadURL 调用 interpreter/download 获取沙箱文件下载直链（pdf/txt 等）。

func (c *Client) resolvePDFDownloadURL(conversationID, messageID, sandboxPath string) (string, error) {

	apiPath := "/backend-api/conversation/" + conversationID + "/interpreter/download"

	body := map[string]interface{}{

		"message_id":   messageID,

		"sandbox_path": sandboxPath,

	}

	resp, err := c.httpClient.R().

		SetHeaders(map[string]string{

			"Content-Type":          "application/json",

			"x-openai-target-path":  apiPath,

			"x-openai-target-route": "/backend-api/conversation/{conversation_id}/interpreter/download",

		}).

		SetBody(body).

		Post(apiPath)

	if err != nil {

		return "", fmt.Errorf("interpreter/download request: %w", err)

	}

	if resp.StatusCode != 200 {

		return "", fmt.Errorf("interpreter/download %d: %s", resp.StatusCode, truncateStr(resp.String(), 200))

	}

	var out struct {

		DownloadURL string `json:"download_url"`

	}

	if err := json.Unmarshal(resp.Bytes(), &out); err != nil {

		return "", fmt.Errorf("parse download response: %w", err)

	}

	if out.DownloadURL == "" {

		return "", fmt.Errorf("empty download_url")

	}

	return out.DownloadURL, nil

}



// ProxyPDFBySandboxPath 代理下载沙箱文件并写入 ResponseWriter。

func (c *Client) ProxyPDFBySandboxPath(conversationID, messageID, sandboxPath string, w interface{}, reqUserAgent string) error {

	writer, ok := w.(http.ResponseWriter)

	if !ok {

		return fmt.Errorf("invalid ResponseWriter")

	}



	downloadURL, err := c.resolvePDFDownloadURL(conversationID, messageID, sandboxPath)

	if err != nil {

		return err

	}



	ua := reqUserAgent

	if ua == "" {

		ua = c.userAgent

	}

	resp, err := req.C().ImpersonateChrome().R().
		SetHeader("User-Agent", ua).
		Get(downloadURL)

	if err != nil {

		return fmt.Errorf("download file: %w", err)

	}

	if resp.StatusCode != 200 {

		return fmt.Errorf("download file %d", resp.StatusCode)

	}



	data := resp.Bytes()

	filename := sandboxPath[strings.LastIndex(sandboxPath, "/")+1:]

	writer.Header().Set("Content-Disposition", "attachment; filename=\""+url.PathEscape(filename)+"\"")

	if strings.HasSuffix(strings.ToLower(filename), ".pdf") {

		writer.Header().Set("Content-Type", "application/pdf")

	} else {

		writer.Header().Set("Content-Type", "application/octet-stream")

	}

	_, err = writer.Write(data)

	return err

}

// DownloadSandboxFile 下载沙箱产物二进制（供 base64 流式下发）。
func (c *Client) DownloadSandboxFile(conversationID, messageID, sandboxPath string) ([]byte, string, error) {
	downloadURL, err := c.resolvePDFDownloadURL(conversationID, messageID, sandboxPath)
	if err != nil {
		return nil, "", err
	}
	ua := c.userAgent
	if ua == "" {
		ua = "Mozilla/5.0"
	}
	resp, err := req.C().ImpersonateChrome().R().SetHeader("User-Agent", ua).Get(downloadURL)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("download file %d", resp.StatusCode)
	}
	filename := sandboxPath[strings.LastIndex(sandboxPath, "/")+1:]
	mime := guessMimeFromName(filename)
	return resp.Bytes(), mime, nil
}

// encodeSandboxPathForQuery URL 编码 sandbox_path。

func encodeSandboxPathForQuery(sandboxPath string) string {

	return url.QueryEscape(sandboxPath)

}

