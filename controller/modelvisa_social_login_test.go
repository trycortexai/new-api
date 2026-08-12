package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelVisaRejectsDirectSocialLoginHandlers(t *testing.T) {
	enableModelVisaHostPolicy(t)
	previousTelegramEnabled := common.TelegramOAuthEnabled
	previousWeChatEnabled := common.WeChatAuthEnabled
	common.TelegramOAuthEnabled = true
	common.WeChatAuthEnabled = true
	t.Cleanup(func() {
		common.TelegramOAuthEnabled = previousTelegramEnabled
		common.WeChatAuthEnabled = previousWeChatEnabled
	})

	handlers := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "Telegram", path: "/api/oauth/telegram/login", handler: TelegramLogin},
		{name: "WeChat", path: "/api/oauth/wechat?code=unused", handler: WeChatAuth},
	}

	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, tt.path, nil)
			context.Request.Host = modelVisaHost

			tt.handler(context)

			var payload map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, false, payload["success"])
			assert.Contains(t, payload["message"], "ModelVisa 暂不支持")
		})
	}
}

func TestModelVisaRejectsSocialAccountBindingHandlers(t *testing.T) {
	enableModelVisaHostPolicy(t)
	previousTelegramEnabled := common.TelegramOAuthEnabled
	previousWeChatEnabled := common.WeChatAuthEnabled
	common.TelegramOAuthEnabled = true
	common.WeChatAuthEnabled = true
	t.Cleanup(func() {
		common.TelegramOAuthEnabled = previousTelegramEnabled
		common.WeChatAuthEnabled = previousWeChatEnabled
	})

	handlers := []struct {
		name    string
		method  string
		path    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "Telegram start", method: http.MethodPost, path: "/api/oauth/telegram/bind/start", handler: TelegramBindStart},
		{name: "Telegram callback", method: http.MethodGet, path: "/api/oauth/telegram/bind/unused", handler: TelegramBind},
		{name: "WeChat", method: http.MethodPost, path: "/api/oauth/wechat/bind", body: `{"code":"unused"}`, handler: WeChatBind},
	}

	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			context.Request.Host = modelVisaHost

			tt.handler(context)

			var payload map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, false, payload["success"])
			assert.Contains(t, payload["message"], "ModelVisa 暂不支持绑定")
		})
	}
}
