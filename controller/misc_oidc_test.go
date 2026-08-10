package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStatusReturnsEffectiveOIDCDisplayName(t *testing.T) {
	settings := system_setting.GetOIDCSettings()
	originalDisplayName := settings.DisplayName
	originalOptionMap := common.OptionMap
	t.Cleanup(func() {
		settings.DisplayName = originalDisplayName
		common.OptionMap = originalOptionMap
	})
	common.OptionMap = map[string]string{}

	tests := []struct {
		name        string
		displayName string
		want        string
	}{
		{
			name:        "custom name is trimmed",
			displayName: "  Acme SSO  ",
			want:        "Acme SSO",
		},
		{
			name:        "whitespace-only name falls back",
			displayName: "   ",
			want:        "OIDC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings.DisplayName = tt.displayName
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

			GetStatus(context)

			var payload struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.True(t, payload.Success)
			assert.Equal(t, tt.want, payload.Data["oidc_display_name"])
		})
	}
}

func TestGetStatusAdvertisesNormalizedOAuthCallbackPolicy(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	previousSecure := common.SessionCookieSecure
	previousTrustedURLs := common.SessionCookieTrustedURLs
	previousOptionMap := common.OptionMap
	system_setting.ServerAddress = "https://NEWAPI.withcortex.ai/"
	common.SessionCookieSecure = true
	common.SessionCookieTrustedURLs = []string{
		"https://LLMAPI.withcortex.ai/",
		"https://newapicn.withcortex.ai",
		"https://llmapi.withcortex.ai",
		"https://newapi.withcortex.ai",
	}
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrustedURLs
		common.OptionMap = previousOptionMap
	})

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

	GetStatus(context)

	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			CanonicalOrigin       string   `json:"oauth_canonical_origin"`
			TrustedOrigins        []string `json:"oauth_trusted_origins"`
			TrustedAliasProviders []string `json:"oauth_trusted_alias_providers"`
			SessionCookieSecure   bool     `json:"session_cookie_secure"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	assert.Equal(t, "https://newapi.withcortex.ai", payload.Data.CanonicalOrigin)
	assert.Equal(t, []string{"https://llmapi.withcortex.ai", "https://newapicn.withcortex.ai"}, payload.Data.TrustedOrigins)
	assert.Equal(t, []string{"github", "discord", "oidc"}, payload.Data.TrustedAliasProviders)
	assert.True(t, payload.Data.SessionCookieSecure)
}
