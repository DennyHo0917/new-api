package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const weComRechargeWebhookEnv = "WECOM_RECHARGE_WEBHOOK_URL"

var weComRechargeHTTPClient = &http.Client{Timeout: 5 * time.Second}

func notifyTopUpSuccess(topUp *TopUp, creditedQuota int) {
	webhookURL := common.GetSecretEnv(weComRechargeWebhookEnv)
	if webhookURL == "" || topUp == nil || creditedQuota <= 0 {
		return
	}

	var user User
	if err := DB.Select("username").First(&user, topUp.UserId).Error; err != nil {
		common.SysError("failed to load user for recharge notification: " + err.Error())
		return
	}

	clean := func(value string) string {
		return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	}
	channel := clean(topUp.PaymentMethod)
	provider := clean(topUp.PaymentProvider)
	if provider != "" && provider != channel {
		channel += " (" + provider + ")"
	}
	payload, err := common.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": fmt.Sprintf("### 用户充值成功\n> 用户名：%s\n> 充值金额：$%.2f\n> 充值渠道：%s", clean(user.Username), float64(creditedQuota)/common.QuotaPerUnit, channel),
		},
	})
	if err != nil {
		common.SysError("failed to encode recharge notification: " + err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		common.SysError("failed to create recharge notification request: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := weComRechargeHTTPClient.Do(req)
	if err != nil {
		common.SysError("failed to send recharge notification: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		common.SysError(fmt.Sprintf("recharge notification returned HTTP %d", resp.StatusCode))
		return
	}
	var result struct {
		ErrorCode int    `json:"errcode"`
		Message   string `json:"errmsg"`
	}
	if err := common.DecodeJson(io.LimitReader(resp.Body, 4096), &result); err != nil {
		common.SysError("failed to decode recharge notification response: " + err.Error())
		return
	}
	if result.ErrorCode != 0 {
		common.SysError(fmt.Sprintf("recharge notification rejected: code=%d message=%s", result.ErrorCode, result.Message))
	}
}
