package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsRetiredFrontendTheme(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"theme.frontend","value":"classic"}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"success":false,"message":"Classic 前端已移除，主题只能设置为 default"}`, response.Body.String())
}

func TestGetStatusAdvertisesDefaultDashboard(t *testing.T) {
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = previousMap })
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

	GetStatus(context)

	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Equal(t, "default", payload.Data["theme"])
}

func TestGetStatusAppliesModelVisaBrandOnlyToModelVisaHost(t *testing.T) {
	previousFooter := common.Footer
	previousMap := common.OptionMap
	previousServerAddress := system_setting.ServerAddress
	previousPasswordLoginEnabled := common.PasswordLoginEnabled
	previousGitHubOAuthEnabled := common.GitHubOAuthEnabled
	previousLinuxDOOAuthEnabled := common.LinuxDOOAuthEnabled
	previousTelegramOAuthEnabled := common.TelegramOAuthEnabled
	previousWeChatAuthEnabled := common.WeChatAuthEnabled
	passkeySetting := system_setting.GetPasskeySettings()
	previousPasskeyEnabled := passkeySetting.Enabled
	common.Footer = `<p>Cortex gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	common.PasswordLoginEnabled = true
	common.GitHubOAuthEnabled = true
	common.LinuxDOOAuthEnabled = true
	common.TelegramOAuthEnabled = true
	common.WeChatAuthEnabled = true
	passkeySetting.Enabled = true
	t.Cleanup(func() {
		common.Footer = previousFooter
		common.OptionMap = previousMap
		system_setting.ServerAddress = previousServerAddress
		common.PasswordLoginEnabled = previousPasswordLoginEnabled
		common.GitHubOAuthEnabled = previousGitHubOAuthEnabled
		common.LinuxDOOAuthEnabled = previousLinuxDOOAuthEnabled
		common.TelegramOAuthEnabled = previousTelegramOAuthEnabled
		common.WeChatAuthEnabled = previousWeChatAuthEnabled
		passkeySetting.Enabled = previousPasskeyEnabled
	})

	tests := []struct {
		name          string
		host          string
		systemName    string
		logo          string
		serverAddress string
		footer        string
		modelVisa     bool
	}{
		{
			name:          "exact host",
			host:          "modelvisa.com",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
			modelVisa:     true,
		},
		{
			name:          "host with port",
			host:          "modelvisa.com:443",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
			modelVisa:     true,
		},
		{
			name:          "case and trailing dot",
			host:          "MoDeLvIsA.CoM.",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
			modelVisa:     true,
		},
		{
			name:          "case trailing dot and port",
			host:          "MoDeLvIsA.CoM.:8443",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
			modelVisa:     true,
		},
		{
			name:          "subdomain does not match",
			host:          "app.modelvisa.com",
			systemName:    common.SystemName,
			logo:          common.Logo,
			serverAddress: system_setting.ServerAddress,
			footer:        common.Footer,
		},
		{
			name:          "different host does not match",
			host:          "newapi.withcortex.ai",
			systemName:    common.SystemName,
			logo:          common.Logo,
			serverAddress: system_setting.ServerAddress,
			footer:        common.Footer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
			context.Request.Host = tt.host

			GetStatus(context)

			var payload struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.True(t, payload.Success)
			assert.Equal(t, tt.systemName, payload.Data["system_name"])
			assert.Equal(t, tt.logo, payload.Data["logo"])
			assert.Equal(t, tt.serverAddress, payload.Data["server_address"])
			assert.Equal(t, tt.footer, payload.Data["footer_html"])
			assert.True(t, payload.Data["password_login_enabled"].(bool))
			if tt.modelVisa {
				assert.False(t, payload.Data["github_oauth"].(bool))
				assert.False(t, payload.Data["discord_oauth"].(bool))
				assert.False(t, payload.Data["linuxdo_oauth"].(bool))
				assert.False(t, payload.Data["telegram_oauth"].(bool))
				assert.False(t, payload.Data["wechat_login"].(bool))
				assert.False(t, payload.Data["oidc_enabled"].(bool))
				assert.False(t, payload.Data["passkey_login"].(bool))
				assert.Empty(t, payload.Data["oauth_trusted_alias_providers"])
			} else {
				assert.True(t, payload.Data["github_oauth"].(bool))
				assert.True(t, payload.Data["passkey_login"].(bool))
				assert.Equal(t, []any{"github", "discord", "oidc"}, payload.Data["oauth_trusted_alias_providers"])
			}
		})
	}
}

func TestGetHomePageContentAppliesModelVisaBrandOnlyToModelVisaHost(t *testing.T) {
	previousMap := common.OptionMap
	canonicalContent := `<h1>Cortex</h1><p>Cortex API and cortex client</p><p>New API by QuantumNous</p>`
	common.OptionMap = map[string]string{"HomePageContent": canonicalContent}
	t.Cleanup(func() { common.OptionMap = previousMap })

	tests := []struct {
		name     string
		host     string
		expected string
	}{
		{
			name:     "exact host",
			host:     "modelvisa.com",
			expected: `<h1>ModelVisa</h1><p>ModelVisa API and cortex client</p><p>New API by QuantumNous</p>`,
		},
		{
			name:     "normalized host",
			host:     "MODELvisa.com.:443",
			expected: `<h1>ModelVisa</h1><p>ModelVisa API and cortex client</p><p>New API by QuantumNous</p>`,
		},
		{
			name:     "subdomain preserves canonical content",
			host:     "www.modelvisa.com",
			expected: canonicalContent,
		},
		{
			name:     "different host preserves canonical content",
			host:     "llmapi.withcortex.ai",
			expected: canonicalContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/home_page_content", nil)
			context.Request.Host = tt.host

			GetHomePageContent(context)

			var payload struct {
				Success bool   `json:"success"`
				Data    string `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.True(t, payload.Success)
			assert.Equal(t, tt.expected, payload.Data)
		})
	}
}

func TestAccountEmailBrandingFollowsRequestHost(t *testing.T) {
	previousSystemName := common.SystemName
	previousServerAddress := system_setting.ServerAddress
	common.SystemName = "Cortex"
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	t.Cleanup(func() {
		common.SystemName = previousSystemName
		system_setting.ServerAddress = previousServerAddress
	})

	tests := []struct {
		name              string
		host              string
		wantName          string
		wantServerAddress string
	}{
		{name: "exact ModelVisa host", host: "modelvisa.com", wantName: modelVisaName, wantServerAddress: modelVisaServerAddress},
		{name: "normalized ModelVisa host", host: "MODELvisa.com.:443", wantName: modelVisaName, wantServerAddress: modelVisaServerAddress},
		{name: "subdomain stays canonical", host: "www.modelvisa.com", wantName: "Cortex", wantServerAddress: "https://newapi.withcortex.ai"},
		{name: "canonical host stays canonical", host: "newapi.withcortex.ai", wantName: "Cortex", wantServerAddress: "https://newapi.withcortex.ai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			brand := accountEmailBrandForHost(tt.host)
			assert.Equal(t, tt.wantName, brand.Name)
			assert.Equal(t, tt.wantServerAddress, brand.ServerAddress)

			verificationSubject, verificationContent := buildVerificationEmail(brand, "123456")
			assert.Contains(t, verificationSubject, tt.wantName)
			assert.Contains(t, verificationContent, tt.wantName)

			resetSubject, resetContent := buildPasswordResetEmail(brand, "user@example.com", "reset-token")
			assert.Contains(t, resetSubject, tt.wantName)
			assert.Contains(t, resetContent, tt.wantName)
			assert.Contains(t, resetContent, tt.wantServerAddress+"/user/reset?email=user@example.com&token=reset-token")
		})
	}
}
