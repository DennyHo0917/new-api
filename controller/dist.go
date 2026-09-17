package controller

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

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
	cryptoCfg := operation_setting.GetCryptoSetting()
	stripeEnabled := isStripeTopUpEnabled()

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
			"enable_crypto_topup": cryptoCfg.EnableCrypto,
			"enable_stripe_topup": stripeEnabled,
			"enable_creem_topup":  false,
			"allow_sub_dist":      false,
			"oauth_origin":        strings.TrimRight(system_setting.ServerAddress, "/"),
			"oauth_providers":     oauthProviders,
			"currency": gin.H{
				"code":              "CNY",
				"symbol":            "¥",
				"exchange_rate":     7.0,
				"usd_exchange_rate": 7.0,
			},
		},
	})
}

// DistGetSiteModels handles GET /api/dist/site/models
func DistGetSiteModels(c *gin.Context) {
	models := service.GetGroupsEnabledModels([]string{"default"})
	if len(models) == 0 {
		models = []string{
			"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022",
			"gemini-1.5-pro", "gemini-1.5-flash", "deepseek-chat", "deepseek-coder",
		}
	}

	type ModelItem struct {
		Id          int    `json:"id"`
		ModelName   string `json:"model_name"`
		DisplayName string `json:"display_name"`
		Enabled     bool   `json:"enabled"`
	}

	items := make([]ModelItem, 0, len(models))
	for i, m := range models {
		items = append(items, ModelItem{
			Id:          i + 1,
			ModelName:   m,
			DisplayName: m,
			Enabled:     true,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}

// DistGetSitePricing handles GET /api/dist/site/pricing
func DistGetSitePricing(c *gin.Context) {
	pricing := model.GetPricing()
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"data":        pricing,
		"group_ratio": ratio_setting.GetGroupRatioCopy(),
	})
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
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    []any{},
	})
}

// DistGetSiteKeyGroupPricing handles GET /api/dist/site/key-groups/:id/pricing
func DistGetSiteKeyGroupPricing(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    []any{},
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
	stripeEnabled := isStripeTopUpEnabled()
	epayEnabled := isEpayTopUpEnabled()
	payMethods := make([]gin.H, 0, len(operation_setting.PayMethods)+2)
	if cryptoCfg.EnableCrypto {
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
			"enable_crypto_topup":   cryptoCfg.EnableCrypto,
			"enable_stripe_topup":   stripeEnabled,
			"enable_creem_topup":    false,
			"crypto_expiry_minutes": cryptoCfg.CryptoExpiryMinutes,
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
			"id":            user.Id,
			"username":      user.Username,
			"display_name":  user.DisplayName,
			"email":         user.Email,
			"role":          user.Role,
			"status":        user.Status,
			"group":         user.Group,
			"quota":         user.Quota,
			"used_quota":    user.UsedQuota,
			"request_count": user.RequestCount,
			"aff_code":      user.AffCode,
			"aff_count":     user.AffCount,
			"aff_quota":     user.AffQuota,
		},
	})
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
			"quota":         user.Quota,
			"used_quota":    user.UsedQuota,
			"request_count": user.RequestCount,
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
		Id             int    `json:"id"`
		Name           string `json:"name"`
		Key            string `json:"key"`
		Status         int    `json:"status"`
		RemainQuota    int64  `json:"remain_quota"`
		UnlimitedQuota bool   `json:"unlimited_quota"`
		CreatedTime    int64  `json:"created_time"`
		AccessedTime   int64  `json:"accessed_time"`
		ExpiredTime    int64  `json:"expired_time"`
		Models         string `json:"models"`
	}

	items := make([]DistTokenItem, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, DistTokenItem{
			Id:             t.Id,
			Name:           t.Name,
			Key:            "sk-" + t.Key,
			Status:         t.Status,
			RemainQuota:    int64(t.RemainQuota),
			UnlimitedQuota: t.UnlimitedQuota,
			CreatedTime:    t.CreatedTime,
			AccessedTime:   t.AccessedTime,
			ExpiredTime:    t.ExpiredTime,
			Models:         t.ModelLimits,
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
		Name           string `json:"name"`
		RemainQuota    int64  `json:"remain_quota"`
		ExpiredTime    int64  `json:"expired_time"`
		UnlimitedQuota bool   `json:"unlimited_quota"`
		Models         string `json:"models"`
		Subnet         string `json:"subnet"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数错误"})
		return
	}

	if req.Name == "" {
		req.Name = "Default API Key"
	}

	rawKey := common.GetRandomString(48)
	cleanToken := model.Token{
		UserId:             userId,
		Name:               req.Name,
		Key:                rawKey,
		CreatedTime:        common.GetTimestamp(),
		AccessedTime:       common.GetTimestamp(),
		ExpiredTime:        req.ExpiredTime,
		RemainQuota:        int(req.RemainQuota),
		UnlimitedQuota:     req.UnlimitedQuota,
		ModelLimits:        req.Models,
		ModelLimitsEnabled: req.Models != "",
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
		Name           *string `json:"name"`
		Status         *int    `json:"status"`
		RemainQuota    *int64  `json:"remain_quota"`
		UnlimitedQuota *bool   `json:"unlimited_quota"`
		ExpiredTime    *int64  `json:"expired_time"`
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
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    []string{},
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

// DistInvoiceInfo handles GET /api/dist/invoice/info
func DistInvoiceInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled": false,
		},
	})
}

// DistInvoiceHistory handles GET /api/dist/invoice/history
func DistInvoiceHistory(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": []any{},
			"total": 0,
		},
	})
}

// DistCreateInvoice handles POST /api/dist/invoice
func DistCreateInvoice(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "发票系统维护中",
	})
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
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"aff_code":          user.AffCode,
			"aff_count":         user.AffCount,
			"aff_quota":         user.AffQuota,
			"aff_history_quota": user.AffHistoryQuota,
		},
	})
}

// DistAffTransfer handles POST /api/dist/aff_transfer
func DistAffTransfer(c *gin.Context) {
	TransferAffQuota(c)
}

// DistAffEarnings handles GET /api/dist/aff_earnings
func DistAffEarnings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": []any{},
			"total": 0,
		},
	})
}

// DistAffPayouts handles GET /api/dist/aff_payouts
func DistAffPayouts(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": []any{},
			"total": 0,
		},
	})
}

// DistAffWithdraw handles POST /api/dist/aff_withdraw
func DistAffWithdraw(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "提现申请已关闭",
	})
}

// DistKolApply handles POST /api/dist/kol_apply
func DistKolApply(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "申请已提交",
	})
}

// DistKolStatus handles GET /api/dist/kol_status
func DistKolStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"status": "none",
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
