package controller

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const oauthAuthFlowTTL = 10 * time.Minute

var oauthTrustedAliasProviders = []string{"github", "discord", "oidc"}

func oauthConfiguredTrustedOrigins() []string {
	canonicalOrigin, _ := common.NormalizeOrigin(system_setting.ServerAddress)
	seen := map[string]struct{}{}
	origins := make([]string, 0, len(common.SessionCookieTrustedURLs))
	for _, rawOrigin := range common.SessionCookieTrustedURLs {
		origin, err := common.NormalizeOrigin(rawOrigin)
		if err != nil || origin == canonicalOrigin {
			continue
		}
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

type oauthStateRequest struct {
	Provider       string `json:"provider"`
	Intent         string `json:"intent"`
	Aff            string `json:"aff,omitempty"`
	RedirectOrigin string `json:"redirect_origin,omitempty"`
}

type oauthFlowPayload struct {
	AffiliateCode string `json:"affiliate_code,omitempty"`
	RedirectURI   string `json:"redirect_uri"`
}

func isConfiguredOAuthOrigin(origin string) bool {
	canonicalOrigin, err := common.NormalizeOrigin(system_setting.ServerAddress)
	if err == nil && origin == canonicalOrigin {
		return true
	}
	for _, trustedOrigin := range common.SessionCookieTrustedURLs {
		normalizedTrustedOrigin, err := common.NormalizeOrigin(trustedOrigin)
		if err == nil && origin == normalizedTrustedOrigin {
			return true
		}
	}
	return false
}

func isHTTPLoopbackOrigin(origin string) bool {
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme != "http" {
		return false
	}
	hostname := strings.ToLower(parsedOrigin.Hostname())
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func isInsecureLocalDevelopmentOAuthOrigin(origin string) bool {
	// The documented Rsbuild proxy runs on a different loopback port from the
	// backend. Keep that callback same-origin with the dev UI so login storage
	// and the binding popup handshake continue to work. This exception cannot
	// activate for an HTTPS or non-loopback deployment.
	if common.SessionCookieSecure || !isHTTPLoopbackOrigin(origin) {
		return false
	}
	canonicalOrigin, err := common.NormalizeOrigin(system_setting.ServerAddress)
	return err == nil && isHTTPLoopbackOrigin(canonicalOrigin)
}

func isAllowedOAuthOrigin(origin string) bool {
	return isConfiguredOAuthOrigin(origin) || isInsecureLocalDevelopmentOAuthOrigin(origin)
}

func oauthTrustedAliasProvidersForHost(host string) []string {
	if isModelVisaHost(host) {
		return nil
	}
	return oauthTrustedAliasProviders
}

func oauthTrustedAliasProvidersForOrigin(origin string) []string {
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return nil
	}
	return oauthTrustedAliasProvidersForHost(parsedOrigin.Host)
}

func oauthProviderSupportsTrustedAlias(provider, origin string) bool {
	if oauth.IsCustomProvider(provider) {
		return false
	}
	for _, aliasProvider := range oauthTrustedAliasProvidersForOrigin(origin) {
		if provider == aliasProvider {
			return true
		}
	}
	return false
}

func isOAuthProviderAllowedAtOrigin(provider, origin string) bool {
	canonicalOrigin, err := common.NormalizeOrigin(system_setting.ServerAddress)
	if err == nil && origin == canonicalOrigin {
		return true
	}
	if isInsecureLocalDevelopmentOAuthOrigin(origin) {
		return true
	}
	return oauthProviderSupportsTrustedAlias(provider, origin)
}

func isAllowedOAuthStartOrigin(requestedOrigin string, request *http.Request) bool {
	origin, err := common.NormalizeOrigin(requestedOrigin)
	if err != nil {
		return false
	}
	if isConfiguredOAuthOrigin(origin) {
		return true
	}
	if !isInsecureLocalDevelopmentOAuthOrigin(origin) {
		return false
	}
	// SESSION_COOKIE_TRUSTED_URL intentionally cannot be configured in insecure
	// mode. Bind the one local exception to the browser's exact Origin instead
	// of accepting an arbitrary loopback port supplied only in JSON.
	originValues := request.Header.Values("Origin")
	if len(originValues) != 1 || strings.Contains(originValues[0], ",") {
		return false
	}
	normalizedBrowserOrigin, err := common.NormalizeOrigin(originValues[0])
	return err == nil && normalizedBrowserOrigin == origin
}

func validateOAuthCallbackURI(provider, callbackURI string) (string, error) {
	if strings.TrimSpace(callbackURI) == "" {
		return resolveOAuthCallbackURI(provider, "")
	}
	parsedCallback, err := url.Parse(callbackURI)
	if err != nil || parsedCallback.User != nil || parsedCallback.RawQuery != "" || parsedCallback.Fragment != "" {
		return "", errors.New("OAuth callback URI is invalid")
	}
	expectedPath := "/oauth/" + url.PathEscape(provider)
	if parsedCallback.Path != expectedPath {
		return "", errors.New("OAuth callback path is invalid")
	}
	origin, err := common.NormalizeOrigin(parsedCallback.Scheme + "://" + parsedCallback.Host)
	if err != nil || !isAllowedOAuthOrigin(origin) || !isOAuthProviderAllowedAtOrigin(provider, origin) {
		return "", errors.New("OAuth callback origin is not allowed")
	}
	return origin + expectedPath, nil
}

func oauthCallbackMatchesRequestHost(request *http.Request, callbackURI string) bool {
	if request == nil || isModelVisaHost(request.Host) {
		return false
	}
	parsedCallback, err := url.Parse(callbackURI)
	if err != nil {
		return false
	}
	callbackOrigin, err := common.NormalizeOrigin(parsedCallback.Scheme + "://" + parsedCallback.Host)
	if err != nil {
		return false
	}
	requestOrigin, err := common.NormalizeOrigin(parsedCallback.Scheme + "://" + request.Host)
	return err == nil && requestOrigin == callbackOrigin
}

func resolveOAuthCallbackURI(provider, requestedOrigin string) (string, error) {
	if requestedOrigin == "" {
		requestedOrigin = system_setting.ServerAddress
	}
	origin, err := common.NormalizeOrigin(requestedOrigin)
	if err != nil || !isAllowedOAuthOrigin(origin) || !isOAuthProviderAllowedAtOrigin(provider, origin) {
		return "", errors.New("OAuth redirect origin is not allowed")
	}
	return origin + "/oauth/" + url.PathEscape(provider), nil
}

// providerParams returns map with Provider key for i18n templates
func providerParams(name string) map[string]any {
	return map[string]any{"Provider": name}
}

// GenerateOAuthCode generates a state code for OAuth CSRF protection
func GenerateOAuthCode(c *gin.Context) {
	var request oauthStateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Intent = strings.TrimSpace(request.Intent)
	request.Aff = strings.TrimSpace(request.Aff)
	request.RedirectOrigin = strings.TrimSpace(request.RedirectOrigin)
	if oauth.GetProvider(request.Provider) == nil ||
		(request.Intent != model.AuthFlowIntentLogin && request.Intent != model.AuthFlowIntentBind) ||
		len(request.Aff) > 32 ||
		(request.Intent == model.AuthFlowIntentBind && request.Aff != "") {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if request.RedirectOrigin != "" && !isAllowedOAuthStartOrigin(request.RedirectOrigin, c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgInvalidParams),
		})
		return
	}
	redirectURI, err := resolveOAuthCallbackURI(request.Provider, request.RedirectOrigin)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgInvalidParams),
		})
		return
	}
	userID := 0
	sessionID := ""
	if request.Intent == model.AuthFlowIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "绑定操作需要登录"})
			return
		}
		userID = identity.UserID
		sessionID = identity.SessionID
	}
	payload, err := common.Marshal(oauthFlowPayload{
		AffiliateCode: request.Aff,
		RedirectURI:   redirectURI,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expiresAt := time.Now().Add(oauthAuthFlowTTL)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  request.Provider,
		Intent:    request.Intent,
		UserId:    userID,
		SessionId: sessionID,
		Payload:   string(payload),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"flow_token":   state,
			"expires_at":   expiresAt.Unix(),
			"redirect_uri": redirectURI,
		},
	})
}

// HandleOAuth handles OAuth callback for all standard OAuth providers
func HandleOAuth(c *gin.Context) {
	providerName := c.Param("provider")
	provider := oauth.GetProvider(providerName)
	if provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthUnknownProvider),
		})
		return
	}

	// 1. Validate state (CSRF protection)
	state := c.Query("state")
	pendingFlow, err := model.GetAuthFlow(state, model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
	})
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
		})
		return
	}
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(pendingFlow.Payload, &payload); err != nil {
		common.ApiError(c, err)
		return
	}
	payload.RedirectURI, err = validateOAuthCallbackURI(providerName, payload.RedirectURI)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
		})
		return
	}
	if !oauthCallbackMatchesRequestHost(c.Request, payload.RedirectURI) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
		})
		return
	}

	consumeMatch := model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
		Intent:   pendingFlow.Intent,
	}
	// 2. Bind flows are bound to the live dashboard Session that created them.
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
			})
			return
		}
		consumeMatch.UserId = identity.UserID
		consumeMatch.SessionId = identity.SessionID
	} else if pendingFlow.Intent != model.AuthFlowIntentLogin {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}

	// 3. Check if provider is enabled
	if !provider.IsEnabled() {
		common.ApiErrorI18n(c, i18n.MsgOAuthNotEnabled, providerParams(provider.GetName()))
		return
	}

	// 4. Handle error from provider
	errorCode := c.Query("error")
	if errorCode != "" {
		if _, err := model.ConsumeAuthFlow(state, consumeMatch); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
			return
		}
		errorDescription := c.Query("error_description")
		if errorDescription == "" {
			errorDescription = errorCode
		}
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": errorDescription,
		})
		return
	}
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		handleOAuthBind(c, provider, pendingFlow, state, payload.RedirectURI)
		return
	}

	// 5. Exchange code for token
	code := c.Query("code")
	token, err := provider.ExchangeToken(c.Request.Context(), code, payload.RedirectURI)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// 6. Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}
	_, err = model.ConsumeAuthFlow(state, consumeMatch)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
		return
	}

	// 7. Find or create user
	user, err := findOrCreateOAuthUser(c, provider, oauthUser, payload.AffiliateCode)
	if err != nil {
		if errors.Is(err, model.ErrEmailAlreadyTaken) {
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
			return
		}
		switch err.(type) {
		case *OAuthUserDeletedError:
			common.ApiErrorI18n(c, i18n.MsgOAuthUserDeleted)
		case *OAuthRegistrationDisabledError:
			common.ApiErrorI18n(c, i18n.MsgUserRegisterDisabled)
		case *OAuthEmailAlreadyTakenError:
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
		default:
			common.ApiError(c, err)
		}
		return
	}

	// 8. Check user status
	if user.Status != common.UserStatusEnabled {
		common.ApiErrorI18n(c, i18n.MsgOAuthUserBanned)
		return
	}

	// 9. Setup login
	setupLogin(user, c)
}

// handleOAuthBind handles binding OAuth account to existing user
func handleOAuthBind(c *gin.Context, provider oauth.Provider, pendingFlow *model.AuthFlow, flowToken, redirectURI string) {
	// Exchange code for token
	code := c.Query("code")
	token, err := provider.ExchangeToken(c.Request.Context(), code, redirectURI)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Check if this OAuth account is already bound (check both new ID and legacy ID)
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
		return
	}
	// Also check legacy ID to prevent duplicate bindings during migration period
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
			return
		}
	}

	if _, err := model.ConsumeAuthFlow(flowToken, model.AuthFlowMatch{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  pendingFlow.Provider,
		Intent:    model.AuthFlowIntentBind,
		UserId:    pendingFlow.UserId,
		SessionId: pendingFlow.SessionId,
	}); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
		return
	}

	user := model.User{Id: pendingFlow.UserId}
	err = user.FillUserById()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// Handle binding based on provider type
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: use user_oauth_bindings table
		err = model.UpdateUserOAuthBinding(user.Id, genericProvider.GetProviderId(), oauthUser.ProviderUserID)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	} else {
		// Built-in provider: update user record directly
		provider.SetProviderUserID(&user, oauthUser.ProviderUserID)
		err = user.Update(false)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}

	common.ApiSuccessI18n(c, i18n.MsgOAuthBindSuccess, gin.H{
		"action": "bind",
	})
}

// findOrCreateOAuthUser finds existing user or creates new user
func findOrCreateOAuthUser(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, affiliateCode string) (*model.User, error) {
	user := &model.User{}

	// Check if user already exists with new ID
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if err != nil {
			return nil, err
		}
		// Check if user has been deleted
		if user.Id == 0 {
			return nil, &OAuthUserDeletedError{}
		}
		return user, nil
	}

	// Try to find user with legacy ID (for GitHub migration from login to numeric ID)
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			err := provider.FillUserByProviderID(user, legacyID)
			if err != nil {
				return nil, err
			}
			if user.Id != 0 {
				// Found user with legacy ID, migrate to new ID
				common.SysLog(fmt.Sprintf("[OAuth] Migrating user %d from legacy_id=%s to new_id=%s",
					user.Id, legacyID, oauthUser.ProviderUserID))
				if err := user.UpdateGitHubId(oauthUser.ProviderUserID); err != nil {
					common.SysError(fmt.Sprintf("[OAuth] Failed to migrate user %d: %s", user.Id, err.Error()))
					// Continue with login even if migration fails
				}
				return user, nil
			}
		}
	}

	// User doesn't exist, create new user if registration is enabled
	if !common.RegisterEnabled {
		return nil, &OAuthRegistrationDisabledError{}
	}

	// Set up new user
	user.Username = provider.GetProviderPrefix() + strconv.Itoa(model.GetMaxUserId()+1)

	if oauthUser.Username != "" {
		if exists, err := model.CheckUserExistOrDeleted(oauthUser.Username, ""); err == nil && !exists {
			// 防止索引退化
			if len(oauthUser.Username) <= model.UserNameMaxLength {
				user.Username = oauthUser.Username
			}
		}
	}

	if oauthUser.DisplayName != "" {
		user.DisplayName = oauthUser.DisplayName
	} else if oauthUser.Username != "" {
		user.DisplayName = oauthUser.Username
	} else {
		user.DisplayName = provider.GetName() + " User"
	}
	if oauthUser.Email != "" {
		user.Email = model.NormalizeEmail(oauthUser.Email)
		if err := model.EnsureEmailAvailable(user.Email, 0); err != nil {
			if errors.Is(err, model.ErrEmailAlreadyTaken) {
				return nil, &OAuthEmailAlreadyTakenError{}
			}
			return nil, err
		}
	}
	user.Role = common.RoleCommonUser
	user.Status = common.UserStatusEnabled

	// Handle affiliate code
	inviterId := 0
	if affiliateCode != "" {
		inviterId, _ = model.GetUserIdByAffCode(affiliateCode)
	}

	// Use transaction to ensure user creation and OAuth binding are atomic
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: create user and binding in a transaction
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}

			// Create OAuth binding
			binding := &model.UserOAuthBinding{
				UserId:         user.Id,
				ProviderId:     genericProvider.GetProviderId(),
				ProviderUserId: oauthUser.ProviderUserID,
			}
			if err := model.CreateUserOAuthBindingWithTx(tx, binding); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		// Perform post-transaction tasks (logs, sidebar config, inviter rewards)
		user.FinalizeOAuthUserCreation(inviterId)
	} else {
		// Built-in provider: create user and update provider ID in a transaction
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}

			// Set the provider user ID on the user model and update
			provider.SetProviderUserID(user, oauthUser.ProviderUserID)
			if err := tx.Model(user).Updates(map[string]interface{}{
				"github_id":   user.GitHubId,
				"discord_id":  user.DiscordId,
				"oidc_id":     user.OidcId,
				"linux_do_id": user.LinuxDOId,
				"wechat_id":   user.WeChatId,
				"telegram_id": user.TelegramId,
			}).Error; err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		// Perform post-transaction tasks
		user.FinalizeOAuthUserCreation(inviterId)
	}

	return user, nil
}

// Error types for OAuth
type OAuthUserDeletedError struct{}

func (e *OAuthUserDeletedError) Error() string {
	return "user has been deleted"
}

type OAuthRegistrationDisabledError struct{}

func (e *OAuthRegistrationDisabledError) Error() string {
	return "registration is disabled"
}

type OAuthEmailAlreadyTakenError struct{}

func (e *OAuthEmailAlreadyTakenError) Error() string {
	return "email is already in use"
}

// handleOAuthError handles OAuth errors and returns translated message
func handleOAuthError(c *gin.Context, err error) {
	switch e := err.(type) {
	case *oauth.OAuthError:
		if e.Params != nil {
			common.ApiErrorI18n(c, e.MsgKey, e.Params)
		} else {
			common.ApiErrorI18n(c, e.MsgKey)
		}
	case *oauth.AccessDeniedError:
		common.ApiErrorMsg(c, e.Message)
	case *oauth.TrustLevelError:
		common.ApiErrorI18n(c, i18n.MsgOAuthTrustLevelLow)
	default:
		common.ApiError(c, err)
	}
}
