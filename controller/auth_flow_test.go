package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type authFlowTestOAuthProvider struct {
	exchangeErr          error
	userInfoErr          error
	exchangeCalls        int
	userInfoCalls        int
	exchangedRedirectURI string
}

func (*authFlowTestOAuthProvider) GetName() string { return "Auth Flow Test" }
func (*authFlowTestOAuthProvider) IsEnabled() bool { return true }
func (provider *authFlowTestOAuthProvider) ExchangeToken(_ context.Context, _ string, redirectURI string) (*oauth.OAuthToken, error) {
	provider.exchangeCalls++
	provider.exchangedRedirectURI = redirectURI
	if provider.exchangeErr != nil {
		return nil, provider.exchangeErr
	}
	return &oauth.OAuthToken{}, nil
}
func (provider *authFlowTestOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	provider.userInfoCalls++
	if provider.userInfoErr != nil {
		return nil, provider.userInfoErr
	}
	return &oauth.OAuthUser{ProviderUserID: "external-user"}, nil
}
func (*authFlowTestOAuthProvider) IsUserIDTaken(string) bool                      { return false }
func (*authFlowTestOAuthProvider) FillUserByProviderID(*model.User, string) error { return nil }
func (*authFlowTestOAuthProvider) SetProviderUserID(*model.User, string)          {}
func (*authFlowTestOAuthProvider) GetProviderPrefix() string                      { return "flow_" }

func setupAuthFlowControllerTest(t *testing.T) *authFlowTestOAuthProvider {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	provider := &authFlowTestOAuthProvider{}
	oauth.Register("auth-flow-test", provider)
	t.Cleanup(func() {
		oauth.Unregister("auth-flow-test")
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return provider
}

func TestGenerateOAuthCodeCarriesAffiliateInLoginFlow(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login","aff":"invite-code"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, "invite-code", payload.AffiliateCode)
	assert.Zero(t, flow.UserId)
	assert.Empty(t, flow.SessionId)
}

func TestGenerateOAuthCodeBindsAnExactAllowedCallbackURI(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	oauth.RegisterCustom("auth-flow-custom", provider)
	previousAddress := system_setting.ServerAddress
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "https://newapi.withcortex.ai/"
	common.SessionCookieTrustedURLs = []string{
		"https://newapi.withcortex.ai",
		"https://llmapi.withcortex.ai",
		"https://newapicn.withcortex.ai",
	}
	t.Cleanup(func() {
		oauth.UnregisterCustomProvider("auth-flow-custom")
		system_setting.ServerAddress = previousAddress
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	tests := []struct {
		name           string
		provider       string
		redirectOrigin string
		wantCallback   string
	}{
		{
			name:           "canonical origin",
			provider:       "auth-flow-custom",
			redirectOrigin: "https://newapi.withcortex.ai",
			wantCallback:   "https://newapi.withcortex.ai/oauth/auth-flow-custom",
		},
		{
			name:           "GitHub trusted alias origin",
			provider:       "github",
			redirectOrigin: "https://llmapi.withcortex.ai",
			wantCallback:   "https://llmapi.withcortex.ai/oauth/github",
		},
		{
			name:           "Discord trusted alias origin",
			provider:       "discord",
			redirectOrigin: "https://newapicn.withcortex.ai",
			wantCallback:   "https://newapicn.withcortex.ai/oauth/discord",
		},
		{
			name:           "OIDC trusted alias origin",
			provider:       "oidc",
			redirectOrigin: "https://llmapi.withcortex.ai",
			wantCallback:   "https://llmapi.withcortex.ai/oauth/oidc",
		},
		{
			name:         "missing origin falls back to canonical",
			provider:     "auth-flow-test",
			wantCallback: "https://newapi.withcortex.ai/oauth/auth-flow-test",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"provider":%q,"intent":"login"}`, test.provider)
			if test.redirectOrigin != "" {
				body = fmt.Sprintf(`{"provider":%q,"intent":"login","redirect_origin":%q}`, test.provider, test.redirectOrigin)
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			GenerateOAuthCode(c)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					FlowToken   string `json:"flow_token"`
					RedirectURI string `json:"redirect_uri"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)
			assert.Equal(t, test.wantCallback, response.Data.RedirectURI)

			flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: test.provider, Intent: model.AuthFlowIntentLogin,
			})
			require.NoError(t, err)
			var payload oauthFlowPayload
			require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
			assert.Equal(t, test.wantCallback, payload.RedirectURI)
		})
	}
}

func TestGenerateOAuthCodeRejectsCanonicalOnlyProvidersOnTrustedAliases(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	oauth.RegisterCustom("auth-flow-custom", provider)
	previousAddress := system_setting.ServerAddress
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	common.SessionCookieTrustedURLs = []string{"https://llmapi.withcortex.ai"}
	t.Cleanup(func() {
		oauth.UnregisterCustomProvider("auth-flow-custom")
		system_setting.ServerAddress = previousAddress
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	for _, provider := range []string{"linuxdo", "auth-flow-custom"} {
		t.Run(provider, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := fmt.Sprintf(`{"provider":%q,"intent":"login","redirect_origin":"https://llmapi.withcortex.ai"}`, provider)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			GenerateOAuthCode(c)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestGenerateOAuthCodeRejectsProvidersOnModelVisaAlias(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	common.SessionCookieTrustedURLs = []string{
		"https://llmapi.withcortex.ai",
		modelVisaServerAddress,
	}
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	tests := []struct {
		name           string
		redirectOrigin string
		wantStatus     int
	}{
		{
			name:           "existing trusted alias remains allowed",
			redirectOrigin: "https://llmapi.withcortex.ai",
			wantStatus:     http.StatusOK,
		},
		{
			name:           "ModelVisa alias rejects GitHub",
			redirectOrigin: modelVisaServerAddress,
			wantStatus:     http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := fmt.Sprintf(`{"provider":"github","intent":"login","redirect_origin":%q}`, tt.redirectOrigin)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			GenerateOAuthCode(c)

			assert.Equal(t, tt.wantStatus, recorder.Code)
			callbackURI := tt.redirectOrigin + "/oauth/github"
			validatedCallback, err := validateOAuthCallbackURI("github", callbackURI)
			if tt.wantStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, callbackURI, validatedCallback)
			} else {
				assert.Error(t, err)
				assert.Empty(t, validatedCallback)
			}
		})
	}
}

func TestGenerateOAuthCodeRejectsUntrustedCallbackOrigins(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	common.SessionCookieTrustedURLs = []string{"https://llmapi.withcortex.ai"}
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	tests := []struct {
		name   string
		origin string
	}{
		{name: "suffix attack", origin: "https://llmapi.withcortex.ai.evil.test"},
		{name: "scheme downgrade", origin: "http://llmapi.withcortex.ai"},
		{name: "path-bearing URL", origin: "https://llmapi.withcortex.ai/callback"},
		{name: "unlisted origin", origin: "https://unlisted.withcortex.ai"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := fmt.Sprintf(`{"provider":"auth-flow-test","intent":"login","redirect_origin":%q}`, test.origin)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			GenerateOAuthCode(c)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestGenerateOAuthCodeAllowsExactBrowserLoopbackOriginInInsecureDevelopment(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	oauth.RegisterCustom("auth-flow-custom", provider)
	previousAddress := system_setting.ServerAddress
	previousSecure := common.SessionCookieSecure
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "http://localhost:3000"
	common.SessionCookieSecure = false
	common.SessionCookieTrustedURLs = nil
	t.Cleanup(func() {
		oauth.UnregisterCustomProvider("auth-flow-custom")
		system_setting.ServerAddress = previousAddress
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(
		`{"provider":"auth-flow-custom","intent":"login","redirect_origin":"http://localhost:5173"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", "http://localhost:5173")

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken   string `json:"flow_token"`
			RedirectURI string `json:"redirect_uri"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "http://localhost:5173/oauth/auth-flow-custom", response.Data.RedirectURI)

	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-custom", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, response.Data.RedirectURI, payload.RedirectURI)
}

func TestGenerateOAuthCodeAllowsExactBrowserLoopbackOriginForBinding(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	previousSecure := common.SessionCookieSecure
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "http://localhost:3000"
	common.SessionCookieSecure = false
	common.SessionCookieTrustedURLs = nil
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(
		`{"provider":"linuxdo","intent":"bind","redirect_origin":"http://localhost:5173"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", "http://localhost:5173")
	c.Set("id", 42)
	c.Set("session_id", "session-42")
	c.Set("auth_version", int64(3))
	c.Set("session_version", int64(2))

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken   string `json:"flow_token"`
			RedirectURI string `json:"redirect_uri"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "http://localhost:5173/oauth/linuxdo", response.Data.RedirectURI)

	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "linuxdo", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, flow.UserId)
	assert.Equal(t, "session-42", flow.SessionId)
}

func TestGenerateOAuthCodeRejectsUnsafeDevelopmentCallbackOrigins(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	previousSecure := common.SessionCookieSecure
	previousTrustedURLs := common.SessionCookieTrustedURLs
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})

	tests := []struct {
		name           string
		serverAddress  string
		secure         bool
		redirectOrigin string
		browserOrigin  string
	}{
		{
			name:           "missing browser origin",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "http://localhost:5173",
		},
		{
			name:           "mismatched browser origin",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "http://localhost:5173",
			browserOrigin:  "http://localhost:5174",
		},
		{
			name:           "combined browser origins",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "http://localhost:5173",
			browserOrigin:  "http://localhost:5173,http://localhost:5174",
		},
		{
			name:           "non-loopback requested origin",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "http://example.test:5173",
			browserOrigin:  "http://example.test:5173",
		},
		{
			name:           "localhost suffix attack",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "http://localhost.evil.test:5173",
			browserOrigin:  "http://localhost.evil.test:5173",
		},
		{
			name:           "non-loopback canonical origin",
			serverAddress:  "https://newapi.withcortex.ai",
			redirectOrigin: "http://localhost:5173",
			browserOrigin:  "http://localhost:5173",
		},
		{
			name:           "secure cookie mode",
			serverAddress:  "http://localhost:3000",
			secure:         true,
			redirectOrigin: "http://localhost:5173",
			browserOrigin:  "http://localhost:5173",
		},
		{
			name:           "https development callback",
			serverAddress:  "http://localhost:3000",
			redirectOrigin: "https://localhost:5173",
			browserOrigin:  "https://localhost:5173",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			system_setting.ServerAddress = test.serverAddress
			common.SessionCookieSecure = test.secure
			common.SessionCookieTrustedURLs = nil
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := fmt.Sprintf(`{"provider":"auth-flow-test","intent":"login","redirect_origin":%q}`, test.redirectOrigin)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			if test.browserOrigin != "" {
				c.Request.Header.Set("Origin", test.browserOrigin)
			}

			GenerateOAuthCode(c)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestGenerateOAuthCodeBindsFlowToAuthenticatedSession(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"bind"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42)
	c.Set("session_id", "session-42")
	c.Set("auth_version", int64(3))
	c.Set("session_version", int64(2))

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, flow.UserId)
	assert.Equal(t, "session-42", flow.SessionId)
}

func TestOAuthLoginConsumesFlowOnlyAfterProviderIdentity(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	tests := []struct {
		name        string
		exchangeErr error
		userInfoErr error
	}{
		{name: "exchange failure", exchangeErr: errors.New("exchange failed")},
		{name: "user info failure", userInfoErr: errors.New("user info failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.exchangeErr = test.exchangeErr
			provider.userInfoErr = test.userInfoErr
			token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
				Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
			})
			require.NoError(t, err)

			router := gin.New()
			router.GET("/api/oauth/:provider", HandleOAuth)
			request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+token+"&code=test", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			flow, err := model.GetAuthFlow(token, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
			})
			require.NoError(t, err)
			assert.Nil(t, flow.ConsumedAt)
		})
	}
}

func TestOAuthExchangeUsesTheStateBoundCallbackURI(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	previousGitHubProvider := oauth.GetProvider("github")
	oauth.Register("github", provider)
	previousAddress := system_setting.ServerAddress
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "https://newapi.withcortex.ai"
	common.SessionCookieTrustedURLs = []string{"https://llmapi.withcortex.ai"}
	t.Cleanup(func() {
		oauth.Register("github", previousGitHubProvider)
		system_setting.ServerAddress = previousAddress
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})
	provider.exchangeErr = errors.New("stop after callback capture")
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "github",
		Intent:    model.AuthFlowIntentLogin,
		Payload:   `{"redirect_uri":"https://llmapi.withcortex.ai/oauth/github"}`,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)

	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/github?state="+flowToken+"&code=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, "https://llmapi.withcortex.ai/oauth/github", provider.exchangedRedirectURI)
}

func TestOAuthExchangeUsesTheStateBoundInsecureLoopbackCallbackURI(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	previousSecure := common.SessionCookieSecure
	previousTrustedURLs := common.SessionCookieTrustedURLs
	system_setting.ServerAddress = "http://localhost:3000"
	common.SessionCookieSecure = false
	common.SessionCookieTrustedURLs = nil
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrustedURLs
	})
	provider.exchangeErr = errors.New("stop after callback capture")
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "auth-flow-test",
		Intent:    model.AuthFlowIntentLogin,
		Payload:   `{"redirect_uri":"http://localhost:5173/oauth/auth-flow-test"}`,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)

	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+flowToken+"&code=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, "http://localhost:5173/oauth/auth-flow-test", provider.exchangedRedirectURI)
}

func TestOAuthLoginConsumesFlowAfterProviderIdentityAndOnProviderError(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	provider.exchangeErr = nil
	provider.userInfoErr = nil
	successToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+successToken+"&code=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(successToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)

	providerErrorToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	request = httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+providerErrorToken+"&error=access_denied", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(providerErrorToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)
}

func TestOAuthBindProviderErrorConsumesSessionBoundFlow(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42", Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("session_id", "session-42")
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
		c.Next()
	})
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+flowToken+"&error=access_denied&error_description=cancelled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	_, err = model.GetAuthFlow(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Zero(t, provider.exchangeCalls)
	assert.Zero(t, provider.userInfoCalls)
}
