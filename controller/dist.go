package controller

import (
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// DistGetSiteInfo handles GET /api/dist/site/info
func DistGetSiteInfo(c *gin.Context) {
	cryptoEnabled := isCryptoTopUpEnabled()
	stripeEnabled := isStripeTopUpEnabled()
	usdExchangeRate := service.GetUSDCNYExchangeRate(c.Request.Context(), operation_setting.Price)

	siteName := common.SystemName
	if siteName == "" {
		siteName = "API Route"
	}
	oauthProviders := make([]gin.H, 0, len(distOAuthProviders))
	for _, id := range []string{"google", "github", "x"} {
		provider := oauth.GetProvider(id)
		if provider != nil && provider.IsEnabled() {
			oauthProviders = append(oauthProviders, gin.H{"id": id, "name": provider.GetName()})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"name":                siteName,
			"theme_template":      "claude",
			"enable_topup":        true,
			"enable_online_topup": isEpayTopUpEnabled(),
			"enable_crypto_topup": cryptoEnabled,
			"enable_stripe_topup": stripeEnabled,
			"enable_creem_topup":  false,
			"allow_sub_dist":      false,
			"oauth_origin":        strings.TrimRight(system_setting.ServerAddress, "/"),
			"oauth_providers":     oauthProviders,
			"currency": gin.H{
				"code":              "CNY",
				"symbol":            "¥",
				"exchange_rate":     usdExchangeRate,
				"usd_exchange_rate": usdExchangeRate,
			},
		},
	})
}

// DistGetSiteModels handles GET /api/dist/site/models
func DistGetSiteModels(c *gin.Context) {
	items := append([]model.Pricing(nil), model.GetPricing()...)
	ratios := ratio_setting.GetGroupRatioCopy()
	applyMinimumChannelMultipliers(items, ratios)
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"data":        items,
		"vendors":     model.GetVendors(),
		"group_ratio": ratios,
		"group_order": getGroupDisplayOrder(),
	})
}

// DistGetSitePricing handles GET /api/dist/site/pricing
func DistGetSitePricing(c *gin.Context) {
	pricing := append([]model.Pricing(nil), model.GetPricing()...)
	ratios := ratio_setting.GetGroupRatioCopy()
	applyMinimumChannelMultipliers(pricing, ratios)
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"data":        pricing,
		"vendors":     model.GetVendors(),
		"group_ratio": ratios,
		"group_order": getGroupDisplayOrder(),
	})
}

func distGroupNames() []string {
	return getGroupDisplayOrder()
}

func distGroupName(value string) string {
	value = strings.TrimSpace(value)
	if ratio_setting.ContainsGroupRatio(value) {
		return value
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 1 {
		return ""
	}
	names := distGroupNames()
	if index > len(names) {
		return ""
	}
	return names[index-1]
}

func distGroupPayload(name string, id int) gin.H {
	return gin.H{
		"id":              id,
		"group":           name,
		"name":            name,
		"vendor_category": "Model access",
		"price_discount":  ratio_setting.GetGroupRatio(name),
		"description":     "Models and prices configured by the administrator.",
	}
}

// DistGetSitePackages handles GET /api/dist/site/packages
func DistGetSitePackages(c *gin.Context) {
	packages, err := model.GetAllEnabledPackages()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    packages,
	})
}

// DistGetSiteKeyGroups handles GET /api/dist/site/key-groups
func DistGetSiteKeyGroups(c *gin.Context) {
	pricing := model.GetPricing()
	names := distGroupNames()
	groups := make([]gin.H, 0, len(names))
	for index, name := range names {
		modelCount := 0
		for _, item := range pricing {
			if common.StringsContains(item.EnableGroup, "all") || common.StringsContains(item.EnableGroup, name) {
				modelCount++
			}
		}
		group := distGroupPayload(name, index+1)
		group["model_count"] = modelCount
		group["is_unavailable"] = modelCount == 0
		groups = append(groups, group)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    groups,
	})
}

// DistGetSiteKeyGroupPricing handles GET /api/dist/site/key-groups/:id/pricing
func DistGetSiteKeyGroupPricing(c *gin.Context) {
	groupName := distGroupName(c.Param("id"))
	if groupName == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "分组不存在"})
		return
	}
	ratio := ratio_setting.GetGroupRatio(groupName)
	items := make([]gin.H, 0)
	for _, pricing := range model.GetPricing() {
		if !common.StringsContains(pricing.EnableGroup, "all") && !common.StringsContains(pricing.EnableGroup, groupName) {
			continue
		}
		category := "chat"
		if len(pricing.SupportedEndpointTypes) > 0 {
			category = string(pricing.SupportedEndpointTypes[0])
		}
		item := gin.H{
			"model_name":   pricing.ModelName,
			"display_name": pricing.ModelName,
			"status":       "healthy",
			"category":     category,
			"route_count":  1,
			"has_range":    false,
		}
		if pricing.QuotaType == 1 {
			item["billing_type"] = "per_call"
			item["fixed_price_min"] = pricing.ModelPrice * ratio
			item["fixed_price_max"] = pricing.ModelPrice * ratio
		} else if pricing.BillingMode == "tiered_expr" {
			item["billing_type"] = "tiered_expr"
			item["billing_expr"] = pricing.BillingExpr
		} else {
			base := pricing.ModelRatio * 2 * ratio / 1000
			item["billing_type"] = "per_token"
			item["input_price_min"] = base
			item["input_price_max"] = base
			item["output_price_min"] = base * pricing.CompletionRatio
			item["output_price_max"] = base * pricing.CompletionRatio
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"group":   distGroupPayload(groupName, 0),
			"items":   items,
			"summary": gin.H{"model_count": len(items)},
		},
	})
}

// DistGetSubDistributorInfo handles GET /api/dist/site/sub-distributor/info
func DistGetSubDistributorInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled": false,
		},
	})
}

// DistGetTopupInfo handles GET /api/dist/topup/info
func DistGetTopupInfo(c *gin.Context) {
	cryptoCfg := operation_setting.GetCryptoSetting()
	cryptoEnabled := isCryptoTopUpEnabled()
	stripeEnabled := isStripeTopUpEnabled()
	epayEnabled := isEpayTopUpEnabled()
	payMethods := make([]gin.H, 0, len(operation_setting.PayMethods)+2)
	if cryptoEnabled {
		payMethods = append(payMethods, gin.H{
			"name": "加密货币充值 (Arbitrum One / TRC20)",
			"type": "crypto",
		})
	}
	if epayEnabled {
		for _, method := range operation_setting.PayMethods {
			payMethods = append(payMethods, gin.H{
				"name":      method["name"],
				"type":      method["type"],
				"icon":      method["icon"],
				"min_topup": method["min_topup"],
			})
		}
	}
	if stripeEnabled {
		payMethods = append(payMethods, gin.H{
			"name":      "Stripe",
			"type":      "stripe",
			"min_topup": setting.StripeMinTopUp,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"min_topup":             cryptoCfg.CryptoMinTopUp,
			"enable_online_topup":   epayEnabled,
			"enable_crypto_topup":   cryptoEnabled,
			"enable_stripe_topup":   stripeEnabled,
			"enable_creem_topup":    false,
			"crypto_expiry_minutes": operation_setting.CryptoOrderExpiryMinutes,
			"stripe_min_topup":      setting.StripeMinTopUp,
			"pay_methods":           payMethods,
			"crypto_wallets": gin.H{
				"tron": cryptoCfg.GetWalletAddress("tron"),
				"arb":  cryptoCfg.GetWalletAddress("arb"),
			},
		},
	})
}

// DistCalculateAmount handles POST /api/dist/topup/amount
func DistCalculateAmount(c *gin.Context) {
	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	if req.Amount <= 0 || math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "充值金额无效"})
		return
	}
	quota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromFloat(req.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
	)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "充值金额超出范围"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"amount": req.Amount,
			"quota":  quota,
		},
	})
}

// DistLogin handles POST /api/dist/user/login with dual-track account discovery.
// If local validation fails or user is not found, attempts upstream SubRouter authentication.
// Upon successful upstream authentication, automatically migrates the account and historical keys to local DB.
func DistLogin(c *gin.Context) {
	if !common.PasswordLoginEnabled {
		common.ApiErrorI18n(c, i18n.MsgUserPasswordLoginDisabled)
		return
	}

	var loginRequest LoginRequest
	err := common.DecodeJson(c.Request.Body, &loginRequest)
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}

	username := strings.TrimSpace(loginRequest.Username)
	password := loginRequest.Password
	if common.PasswordLoginEncryptionEnabled {
		if loginRequest.PasswordEncrypted != "" && loginRequest.EncryptionKeyID != "" {
			if decrypted, decErr := common.DecryptPassword(loginRequest.PasswordEncrypted, loginRequest.EncryptionKeyID); decErr == nil {
				password = decrypted
			}
		}
	}

	if username == "" || password == "" {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}

	// 1. Attempt local login first
	localUser := model.User{
		Username: username,
		Password: password,
	}
	err = localUser.ValidateAndFill()
	if err == nil {
		if refreshErr := service.RefreshSubRouterLegacyBalance(&localUser, username, password); refreshErr != nil {
			logger.LogWarn(c, "[Migration] Legacy balance refresh failed")
		}
		setupLogin(&localUser, c)
		return
	}

	// 2. Local authentication failed (user not found or password changed)
	// Try upstream SubRouter authentication and migration
	migratedUser, migrateErr := service.AuthenticateAndMigrateSubRouterUser(username, password)
	if migrateErr == nil && migratedUser != nil {
		logger.LogInfo(c, fmt.Sprintf("User %s successfully authenticated via SubRouter and migrated to local DB", username))
		setupLogin(migratedUser, c)
		return
	}
	migrationFailure := "internal_error"
	switch {
	case errors.Is(migrateErr, service.ErrSubRouterAuthFailed):
		migrationFailure = "upstream_auth_failed"
	case errors.Is(migrateErr, service.ErrSubRouterMigrationNotEligible):
		migrationFailure = "not_eligible"
	case errors.Is(migrateErr, service.ErrSubRouterTokenSyncFailed):
		migrationFailure = "token_sync_failed"
	}
	logger.LogWarn(c, "[Migration] Legacy login fallback failed: "+migrationFailure)

	// 3. Both local and upstream authentication failed
	common.ApiErrorI18n(c, i18n.MsgUserUsernameOrPasswordError)
}

// DistGetUserSelf handles GET /api/dist/user/self
func DistGetUserSelf(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取用户信息失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"id":                      user.Id,
			"username":                user.Username,
			"display_name":            user.DisplayName,
			"avatar_url":              user.AvatarUrl,
			"email":                   user.Email,
			"role":                    user.Role,
			"status":                  user.Status,
			"group":                   user.Group,
			"quota":                   user.DisplayQuota(),
			"local_quota":             user.Quota,
			"legacy_quota":            user.LegacySubRouterQuota(),
			"legacy_quota_updated_at": user.LegacySubRouterQuotaUpdatedAt(),
			"used_quota":              user.UsedQuota,
			"request_count":           user.RequestCount,
			"aff_code":                user.AffCode,
			"aff_count":               user.AffCount,
			"aff_quota":               user.AffQuota,
			"aff_history_quota":       user.AffHistoryQuota,
			"default_commission_rate": model.AffiliateCommissionRate(0),
			"commission_rate":         model.AffiliateCommissionRate(user.AffCount),
		},
	})
}

const distEmailBindingReauthenticationAge = 10 * time.Minute

type distEmailBindingRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func distEmailBindingIdentity(c *gin.Context) (service.AuthIdentity, *model.User, error) {
	identity := service.AuthIdentity{
		UserID:          c.GetInt("id"),
		SessionID:       c.GetString("session_id"),
		UserAuthVersion: c.GetInt64("auth_version"),
		SessionVersion:  c.GetInt64("session_version"),
	}
	if identity.UserID <= 0 || identity.SessionID == "" {
		return service.AuthIdentity{}, nil, service.ErrAuthTokenInvalid
	}
	session, _, err := service.ValidateLoginSession(identity)
	if err != nil {
		return service.AuthIdentity{}, nil, err
	}
	if time.Since(time.Unix(session.CreatedAt, 0)) > distEmailBindingReauthenticationAge {
		return service.AuthIdentity{}, nil, errors.New("recent authentication required")
	}
	user, err := model.GetUserById(identity.UserID, false)
	if err != nil {
		return service.AuthIdentity{}, nil, err
	}
	if model.NormalizeEmail(user.Email) != "" {
		return service.AuthIdentity{}, nil, errors.New("email is already bound")
	}
	return identity, user, nil
}

func distEmailBindingKey(userID int, email string) string {
	return strconv.Itoa(userID) + ":" + email
}

// DistSendEmailBindVerification supports the existing production frontend's
// initial-email binding contract. Email replacement stays on the stronger
// security-proof flow and is deliberately rejected here.
func DistSendEmailBindVerification(c *gin.Context) {
	identity, _, err := distEmailBindingIdentity(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "请重新登录后绑定邮箱"})
		return
	}
	var request distEmailBindingRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	email, err := service.ValidateAccountEmail(request.Email)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	code := common.GenerateVerificationCode(6)
	subject := common.SystemName + " — Confirm your email address"
	content := fmt.Sprintf("<p>Confirm linking this email address to your account.</p><p>Verification code: <strong>%s</strong></p><p>This code expires in %d minutes. If you did not request this change, do not share this code.</p>", html.EscapeString(code), common.VerificationValidMinutes)
	if err := common.SendEmail(subject, email, content); err != nil {
		common.SysError("failed to send email binding verification: " + err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "验证邮件发送失败"})
		return
	}
	if !model.IsEmailAlreadyTaken(email) {
		common.RegisterVerificationCodeWithKey(distEmailBindingKey(identity.UserID, email), code, common.EmailBindingPurpose)
	}
	recordUserSecurityAudit(c, identity.UserID, "user.email_binding_compat_start", map[string]any{"success": true})
	common.ApiSuccess(c, nil)
}

func DistBindUserEmail(c *gin.Context) {
	identity, user, err := distEmailBindingIdentity(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "请重新登录后绑定邮箱"})
		return
	}
	succeeded := false
	defer func() {
		recordUserSecurityAudit(c, identity.UserID, "user.email_binding_compat_bind", map[string]any{"success": succeeded})
	}()
	var request distEmailBindingRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	email, err := service.ValidateAccountEmail(request.Email)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	key := distEmailBindingKey(identity.UserID, email)
	if !common.VerifyCodeWithKey(key, strings.TrimSpace(request.Code), common.EmailBindingPurpose) {
		common.ApiErrorI18n(c, i18n.MsgUserVerificationCodeError)
		return
	}
	if err := model.BindEmailToUser(user, email); err != nil {
		if errors.Is(err, model.ErrEmailAlreadyTaken) {
			common.ApiErrorI18n(c, i18n.MsgUserVerificationCodeError)
			return
		}
		common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		return
	}
	common.DeleteKey(key, common.EmailBindingPurpose)
	succeeded = true
	common.ApiSuccess(c, nil)
}

// DistGetUserUsage handles GET /api/dist/user/usage
func DistGetUserUsage(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"quota":                   user.DisplayQuota(),
			"local_quota":             user.Quota,
			"legacy_quota":            user.LegacySubRouterQuota(),
			"legacy_quota_updated_at": user.LegacySubRouterQuotaUpdatedAt(),
			"used_quota":              user.UsedQuota,
			"request_count":           user.RequestCount,
		},
	})
}

// DistGetTokens handles GET /api/dist/token/list
func DistGetTokens(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	tokens, err := model.GetAllUserTokens(userId, 0, 100)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	type DistTokenItem struct {
		Id                 int    `json:"id"`
		Name               string `json:"name"`
		Key                string `json:"key"`
		Status             int    `json:"status"`
		RemainQuota        int64  `json:"remain_quota"`
		UnlimitedQuota     bool   `json:"unlimited_quota"`
		CreatedTime        int64  `json:"created_time"`
		AccessedTime       int64  `json:"accessed_time"`
		ExpiredTime        int64  `json:"expired_time"`
		ModelLimits        string `json:"model_limits"`
		ModelLimitsEnabled bool   `json:"model_limits_enabled"`
		AllowIps           string `json:"allow_ips"`
		Group              string `json:"group"`
	}

	items := make([]DistTokenItem, 0, len(tokens))
	for _, t := range tokens {
		allowIps := ""
		if t.AllowIps != nil {
			allowIps = *t.AllowIps
		}
		items = append(items, DistTokenItem{
			Id:                 t.Id,
			Name:               t.Name,
			Key:                "sk-" + t.Key,
			Status:             t.Status,
			RemainQuota:        int64(t.RemainQuota),
			UnlimitedQuota:     t.UnlimitedQuota,
			CreatedTime:        t.CreatedTime,
			AccessedTime:       t.AccessedTime,
			ExpiredTime:        t.ExpiredTime,
			ModelLimits:        t.ModelLimits,
			ModelLimitsEnabled: t.ModelLimitsEnabled,
			AllowIps:           allowIps,
			Group:              t.Group,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}

// DistCreateToken handles POST /api/dist/token/create
func DistCreateToken(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	var req struct {
		Name               string `json:"name"`
		RemainQuota        int64  `json:"remain_quota"`
		ExpiredTime        int64  `json:"expired_time"`
		UnlimitedQuota     *bool  `json:"unlimited_quota"`
		ModelLimits        string `json:"model_limits"`
		ModelLimitsEnabled *bool  `json:"model_limits_enabled"`
		AllowIps           string `json:"allow_ips"`
		Group              string `json:"group"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	if req.Name == "" {
		req.Name = "Default API Key"
	}

	rawKey := common.GetRandomString(48)
	unlimitedQuota := true
	if req.UnlimitedQuota != nil {
		unlimitedQuota = *req.UnlimitedQuota
	}
	modelLimitsEnabled := strings.TrimSpace(req.ModelLimits) != ""
	if req.ModelLimitsEnabled != nil {
		modelLimitsEnabled = *req.ModelLimitsEnabled
	}
	allowIps := strings.TrimSpace(req.AllowIps)
	cleanToken := model.Token{
		UserId:             userId,
		Name:               req.Name,
		Key:                rawKey,
		CreatedTime:        common.GetTimestamp(),
		AccessedTime:       common.GetTimestamp(),
		ExpiredTime:        req.ExpiredTime,
		RemainQuota:        int(req.RemainQuota),
		UnlimitedQuota:     unlimitedQuota,
		ModelLimits:        strings.TrimSpace(req.ModelLimits),
		ModelLimitsEnabled: modelLimitsEnabled,
		AllowIps:           &allowIps,
		Group:              strings.TrimSpace(req.Group),
		Status:             common.TokenStatusEnabled,
	}

	if err := cleanToken.Insert(); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "创建令牌失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "success",
		"data": gin.H{
			"id":              cleanToken.Id,
			"name":            cleanToken.Name,
			"key":             "sk-" + cleanToken.Key,
			"status":          cleanToken.Status,
			"remain_quota":    cleanToken.RemainQuota,
			"unlimited_quota": cleanToken.UnlimitedQuota,
			"created_time":    cleanToken.CreatedTime,
			"expired_time":    cleanToken.ExpiredTime,
		},
	})
}

// DistUpdateToken handles PUT /api/dist/token/:id
func DistUpdateToken(c *gin.Context) {
	userId := c.GetInt("id")
	tokenId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 || tokenId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	token, err := model.GetTokenByIds(tokenId, userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "令牌不存在"})
		return
	}

	var req struct {
		Name               *string `json:"name"`
		Status             *int    `json:"status"`
		RemainQuota        *int64  `json:"remain_quota"`
		UnlimitedQuota     *bool   `json:"unlimited_quota"`
		ExpiredTime        *int64  `json:"expired_time"`
		ModelLimits        *string `json:"model_limits"`
		ModelLimitsEnabled *bool   `json:"model_limits_enabled"`
		AllowIps           *string `json:"allow_ips"`
		Group              *string `json:"group"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	if req.Name != nil {
		token.Name = *req.Name
	}
	if req.Status != nil {
		token.Status = *req.Status
	}
	if req.RemainQuota != nil {
		token.RemainQuota = int(*req.RemainQuota)
	}
	if req.UnlimitedQuota != nil {
		token.UnlimitedQuota = *req.UnlimitedQuota
	}
	if req.ExpiredTime != nil {
		token.ExpiredTime = *req.ExpiredTime
	}
	if req.ModelLimits != nil {
		token.ModelLimits = strings.TrimSpace(*req.ModelLimits)
	}
	if req.ModelLimitsEnabled != nil {
		token.ModelLimitsEnabled = *req.ModelLimitsEnabled
	}
	if req.AllowIps != nil {
		allowIps := strings.TrimSpace(*req.AllowIps)
		token.AllowIps = &allowIps
	}
	if req.Group != nil && strings.TrimSpace(*req.Group) != "" {
		token.Group = strings.TrimSpace(*req.Group)
	}

	if err := token.Update(); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "更新令牌失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "更新成功"})
}

// DistDeleteToken handles DELETE /api/dist/token/:id
func DistDeleteToken(c *gin.Context) {
	userId := c.GetInt("id")
	tokenId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 || tokenId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	err := model.DeleteTokenById(tokenId, userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "删除失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "删除成功"})
}

// DistGetTokenModels handles GET /api/dist/token/:id/models
func DistGetTokenModels(c *gin.Context) {
	userId := c.GetInt("id")
	tokenId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 || tokenId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	token, err := model.GetTokenByIds(tokenId, userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "令牌不存在"})
		return
	}
	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}
	group := strings.TrimSpace(token.Group)
	if group == "" {
		group = strings.TrimSpace(user.Group)
	}
	models := service.GetGroupsEnabledModels([]string{group})
	if token.ModelLimitsEnabled && strings.TrimSpace(token.ModelLimits) != "" {
		allowed := make(map[string]bool)
		for item := range strings.SplitSeq(token.ModelLimits, ",") {
			if item = strings.TrimSpace(item); item != "" {
				allowed[item] = true
			}
		}
		filtered := make([]string, 0, len(models))
		for _, item := range models {
			if allowed[item] {
				filtered = append(filtered, item)
			}
		}
		models = filtered
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"models":                  models,
			"count":                   len(models),
			"restricted_by_models":    token.ModelLimitsEnabled,
			"restricted_by_providers": false,
			"provider_names":          []string{},
			"group":                   group,
		},
	})
}

// DistSubscribePackage handles POST /api/dist/package/subscribe
func DistSubscribePackage(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	var req struct {
		PackageId int `json:"package_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	pkg, err := model.GetPackageById(req.PackageId)
	if err != nil || pkg == nil || !pkg.Enabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐不存在或已下架"})
		return
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}

	// Calculate cost in Quota (assume price in CNY, 1 USD = 7 CNY = 500,000 Quota)
	costQuota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromFloat(pkg.Price).
			Div(decimal.NewFromFloat(7)).
			Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
	)
	if err != nil || costQuota < 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "套餐价格无效"})
		return
	}
	if user.Quota < costQuota {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "余额不足，请先充值"})
		return
	}

	// Deduct cost quota and subscribe
	if costQuota > 0 {
		if err := model.DecreaseUserQuota(userId, costQuota, true); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "扣费失败: " + err.Error()})
			return
		}
	}

	sub, err := model.CreatePackageSubscription(userId, pkg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "订阅失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "订阅成功",
		"data":    sub,
	})
}

// DistGetActiveSubscriptions handles GET /api/dist/package/subscriptions
func DistGetActiveSubscriptions(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	subs, err := model.GetUserActiveSubscriptions(userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    subs,
	})
}

// DistGetTopupHistory handles GET /api/dist/topup/history
func DistGetTopupHistory(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	pageInfo := &common.PageInfo{
		Page:     page,
		PageSize: pageSize,
	}

	topUps, total, err := model.GetUserTopUps(userId, pageInfo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": topUps,
			"total": total,
		},
	})
}

// DistRedeemCode handles POST /api/dist/topup/redeem
func DistRedeemCode(c *gin.Context) {
	TopUp(c)
}

// DistGetAffCode handles GET /api/dist/aff
func DistGetAffCode(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": user.AffCode})
}

// DistAffTransfer handles POST /api/dist/aff_transfer
func DistAffTransfer(c *gin.Context) {
	TransferAffQuota(c)
}

// DistAffEarnings handles GET /api/dist/aff_earnings
func DistAffEarnings(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	pageSize = min(pageSize, 100)
	items, total, err := model.GetAffiliateEarnings(userId, &common.PageInfo{Page: page, PageSize: pageSize})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取返佣记录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
		"total":   total,
	})
}

// DistAffPayouts handles GET /api/dist/aff_payouts
func DistAffPayouts(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	pageSize = min(pageSize, 100)
	items, total, err := model.GetAffiliatePayouts(userId, &common.PageInfo{Page: page, PageSize: pageSize})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取提现记录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": items,
			"total": total,
		},
	})
}

// DistAffWithdraw handles POST /api/dist/aff_withdraw
func DistAffWithdraw(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	var req struct {
		Amount        float64 `json:"amount"`
		PaymentMethod string  `json:"payment_method"`
		Remark        string  `json:"remark"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	if math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || req.Amount <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) || common.QuotaPerUnit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "提现金额无效"})
		return
	}
	quota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromFloat(req.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
	)
	if err != nil || quota <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "提现金额无效"})
		return
	}
	payout, err := model.CreateAffiliatePayout(userId, quota, req.PaymentMethod, req.Remark)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrAffiliatePayoutInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "提现参数无效"})
		case errors.Is(err, model.ErrAffiliatePayoutInsufficient):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "邀请额度不足"})
		default:
			common.ApiError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "提现申请已提交",
		"data": gin.H{
			"id":             payout.Id,
			"quota":          payout.Quota,
			"amount":         req.Amount,
			"payment_method": payout.PaymentMethod,
			"remark":         payout.Remark,
			"status":         payout.Status,
			"created_time":   payout.CreatedTime,
		},
	})
}

// DistUserTasks handles GET /api/dist/user/tasks
func DistUserTasks(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": []any{},
			"total": 0,
		},
	})
}

// DistUserMj handles GET /api/dist/user/mj
func DistUserMj(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": []any{},
			"total": 0,
		},
	})
}

// DistUpdateUserPassword handles PUT /api/dist/user/password
func DistUpdateUserPassword(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}

	var req struct {
		OriginalPassword string `json:"original_password"`
		Password         string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	user, err := model.GetUserById(userId, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}

	if !common.ValidatePasswordAndHash(req.OriginalPassword, user.Password) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "当前密码不正确"})
		return
	}

	if len(req.Password) < 8 || len(req.Password) > 20 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "新密码长度需为 8-20 位"})
		return
	}

	hashedPassword, err := common.Password2Hash(req.Password)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "加密失败"})
		return
	}

	if err := model.DB.Model(&model.User{}).Where("id = ?", userId).Update("password", hashedPassword).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "更新失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "密码修改成功"})
}

// DistLogout handles POST /api/dist/user/logout
func DistLogout(c *gin.Context) {
	AuthLogout(c)
}
