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
	common.Footer = `<p>Cortex gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`
	common.OptionMap = map[string]string{}
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	t.Cleanup(func() {
		common.Footer = previousFooter
		common.OptionMap = previousMap
		system_setting.ServerAddress = previousServerAddress
	})

	tests := []struct {
		name          string
		host          string
		systemName    string
		logo          string
		serverAddress string
		footer        string
	}{
		{
			name:          "exact host",
			host:          "modelvisa.com",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
		},
		{
			name:          "host with port",
			host:          "modelvisa.com:443",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
		},
		{
			name:          "case and trailing dot",
			host:          "MoDeLvIsA.CoM.",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
		},
		{
			name:          "case trailing dot and port",
			host:          "MoDeLvIsA.CoM.:8443",
			systemName:    modelVisaName,
			logo:          modelVisaLogoURL,
			serverAddress: modelVisaServerAddress,
			footer:        `<p>ModelVisa gateway</p><p>cortex remains lowercase</p><p>New API by QuantumNous</p>`,
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
