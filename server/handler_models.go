package server

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	sentinel "sentinel-go"
)

type ModelsHandler struct {
	cfg *ServerConfig
}

func NewModelsHandler(cfg *ServerConfig) *ModelsHandler {
	return &ModelsHandler{cfg: cfg}
}

// Handle 从 chatgpt.com 动态返回当前 token/账号可用的模型列表。
func (h *ModelsHandler) Handle(c *gin.Context) {
	token := extractChatGPTToken(c)
	client := sentinel.NewClient(sentinel.Config{BearerToken: token, Model: h.cfg.DefaultModel})
	client.Logf = nil

	available, err := client.GetAvailableModels()
	if err != nil {
		// 模型发现失败不影响聊天可用性；只暴露默认回退模型，避免返回过时的静态清单。
		log.Printf("[models] dynamic discovery failed; fallback_model=%s error=%v", h.cfg.DefaultModel, err)
		c.Header("X-Models-Source", "fallback")
		c.JSON(http.StatusOK, ModelList{
			Object: "list",
			Data: []Model{{
				ID: h.cfg.DefaultModel, Object: "model", Created: time.Now().Unix(), OwnedBy: "chatgpt-fallback",
			}},
		})
		return
	}

	now := time.Now().Unix()
	models := make([]Model, 0, len(available))
	for _, item := range available {
		models = append(models, Model{
			ID: item.Slug, Object: "model", Created: now, OwnedBy: "chatgpt.com",
		})
	}
	log.Printf("[models] dynamic discovery succeeded count=%d", len(models))
	c.Header("X-Models-Source", "chatgpt.com")
	c.JSON(http.StatusOK, ModelList{Object: "list", Data: models})
}
