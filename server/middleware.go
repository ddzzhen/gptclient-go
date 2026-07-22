package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORSMiddleware 跨域中间件
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD, PATCH")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Requested-With")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// AuthMiddleware 鉴权中间件
// 若配置了 AUTHORIZATION 环境变量，则验证请求头中的 Bearer Token 是否匹配
// 若未配置，则跳过鉴权（直接将 Bearer Token 视为 ChatGPT token）
func AuthMiddleware(cfg *ServerConfig, pool *TokenPool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从请求头提取 Bearer Token（兼容 "Bearer eyJ..." 和 "Bearer" 无空格两种情况）
		auth := c.GetHeader("Authorization")
		// 先去掉 "Bearer "（有空格），再去掉 "Bearer"（无空格），最后 TrimSpace
		raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(auth, "Bearer "), "Bearer"))

		// 注意：AUTHORIZATION 密码可以是任意字符串（如 "sk-..."），不能用 cleanToken 校验。
		// cleanToken 仅校验 ChatGPT access token（JWT），会把非 JWT 的密码清成空串，
		// 导致密码匹配永远失败。因此密码比对使用原始值，cleanToken 只在透传 ChatGPT token 时使用。

		// 允许“免密模式”或“密码匹配模式”：
		// - 如果传入的 raw 就是我们配置的 AUTHORIZATION 密码
		// - 如果传入的 raw 为空，且我们没有配置密码（完全开放给本地使用）
		if (cfg.Authorization != "" && raw == cfg.Authorization) || (cfg.Authorization == "" && raw == "") {
			chatgptToken, ok := pool.Pick()
			if !ok {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, ErrorResponse{
					Error: ErrorDetail{
						Message: "Token pool is empty. Please upload tokens or provide one in the request.",
						Type:    "server_error",
						Code:    "no_token",
					},
				})
				return
			}
			c.Set("chatgpt_token", chatgptToken)
			c.Set("from_pool", true)
		} else if cfg.Authorization != "" && raw != "" {
			// 如果配置了密码，且传入了密码，但不匹配
			c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{
				Error: ErrorDetail{
					Message: "Invalid API key",
					Type:    "invalid_request_error",
					Code:    "invalid_api_key",
				},
			})
			return
		} else if raw == "" {
			// 这种情况只有一种：配了密码，但是没有传入 token，此时应该提示需要鉴权
			c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{
				Error: ErrorDetail{
					Message: "Missing Authorization header",
					Type:    "invalid_request_error",
					Code:    "missing_auth",
				},
			})
			return
		} else {
			// 未配置 AUTHORIZATION，且传入了 token，将其作为 ChatGPT Bearer token 透传。
			// cleanToken 可从 JSON / "at----st" 组合中提取 access token；无法识别则原样透传，
			// 避免把合法的非标准 token 误清成空串。
			token := cleanToken(raw)
			if token == "" {
				token = raw
			}
			c.Set("chatgpt_token", token)
			c.Set("from_pool", false)
		}

		c.Next()
	}
}

// extractChatGPTToken 从 gin Context 中取出 chatgpt_token
func extractChatGPTToken(c *gin.Context) string {
	v, _ := c.Get("chatgpt_token")
	t, _ := v.(string)
	return t
}
