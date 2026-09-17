package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

var distOAuthProviders = map[string]struct{}{
	"google": {},
	"github": {},
	"x":      {},
}

func DistOAuthStart(c *gin.Context) {
	providerName := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if _, ok := distOAuthProviders[providerName]; !ok {
		common.ApiErrorI18n(c, i18n.MsgOAuthUnknownProvider)
		return
	}
	provider := oauth.GetProvider(providerName)
	if provider == nil || !provider.IsEnabled() {
		common.ApiErrorI18n(c, i18n.MsgOAuthNotEnabled, providerParams(providerName))
		return
	}

	clientID, authorizationEndpoint, scopes := "", "", ""
	switch typed := provider.(type) {
	case *oauth.GitHubProvider:
		clientID = common.GitHubClientId
		authorizationEndpoint = "https://github.com/login/oauth/authorize"
		scopes = "read:user user:email"
	case *oauth.GenericOAuthProvider:
		config := typed.GetConfig()
		clientID = config.ClientId
		authorizationEndpoint = config.AuthorizationEndpoint
		scopes = config.Scopes
	}
	if clientID == "" || authorizationEndpoint == "" {
		common.ApiErrorI18n(c, i18n.MsgOAuthNotEnabled, providerParams(provider.GetName()))
		return
	}

	serverAddress := strings.TrimRight(strings.TrimSpace(system_setting.ServerAddress), "/")
	redirectURI := serverAddress + "/oauth/" + providerName
	parsedRedirect, err := url.Parse(redirectURI)
	if err != nil || parsedRedirect.Host == "" || (parsedRedirect.Scheme != "https" && parsedRedirect.Scheme != "http") {
		common.ApiErrorI18n(c, i18n.MsgOAuthConnectFailed, providerParams(provider.GetName()))
		return
	}

	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	affiliateCode := strings.TrimSpace(c.Query("aff"))
	if len(affiliateCode) > 32 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	payload, err := common.Marshal(oauthFlowPayload{AffiliateCode: affiliateCode, CodeVerifier: verifier})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  providerName,
		Intent:    model.AuthFlowIntentLogin,
		Payload:   string(payload),
		ExpiresAt: time.Now().Add(oauthAuthFlowTTL),
	})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	authorizeURL, err := url.Parse(authorizationEndpoint)
	if err != nil || authorizeURL.Scheme != "https" || authorizeURL.Host == "" {
		writeSecurityOperationError(c, errors.New("invalid OAuth authorization endpoint"))
		return
	}
	query := authorizeURL.Query()
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", scopes)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, authorizeURL.String())
}

func DistOAuthCallback(c *gin.Context) {
	HandleOAuth(c)
}
