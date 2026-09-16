package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type CreateCryptoOrderRequest struct {
	Amount float64 `json:"amount" binding:"required"`
	Chain  string  `json:"chain" binding:"required"`
	Token  string  `json:"token" binding:"required"`
}

type SubmitCryptoTxHashRequest struct {
	TradeNo string `json:"trade_no" binding:"required"`
	TxHash  string `json:"tx_hash" binding:"required"`
}

// CreateCryptoOrder handles POST /api/dist/topup/crypto/pay
func CreateCryptoOrder(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "请先登录"})
		return
	}

	var req CreateCryptoOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "参数错误: " + err.Error()})
		return
	}

	cfg := operation_setting.GetCryptoSetting()
	if !cfg.EnableCrypto {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "加密货币充值暂未开放"})
		return
	}

	if req.Amount < cfg.CryptoMinTopUp {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "error",
			"data":    fmt.Sprintf("充值金额不能小于最低充值限制: %.2f", cfg.CryptoMinTopUp),
		})
		return
	}

	chain := strings.ToLower(strings.TrimSpace(req.Chain))
	token := strings.ToUpper(strings.TrimSpace(req.Token))

	walletAddress := cfg.GetWalletAddress(chain)
	if walletAddress == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "error",
			"data":    fmt.Sprintf("平台未配置 %s 网络的收款钱包，请联系管理员", chain),
		})
		return
	}

	contractAddress := cfg.GetContractAddress(chain, token)
	if contractAddress == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "error",
			"data":    fmt.Sprintf("平台未支持 %s 网络上的 %s 代币", chain, token),
		})
		return
	}

	tradeNo := fmt.Sprintf("CRYPTO%s%d", common.GetRandomString(6), time.Now().Unix())
	expiryDuration := time.Duration(cfg.CryptoExpiryMinutes) * time.Minute
	if expiryDuration <= 0 {
		expiryDuration = 30 * time.Minute
	}
	expiredAt := time.Now().Add(expiryDuration).Unix()

	order := &model.CryptoTransaction{
		TradeNo:        tradeNo,
		UserId:         userId,
		Chain:          chain,
		Token:          token,
		WalletAddress:  walletAddress,
		ExpectedAmount: req.Amount,
		Status:         model.CryptoStatusPending,
		ExpiredAt:      expiredAt,
	}

	if err := model.CreateCryptoTransaction(order); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "创建充值订单失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "success",
		"data": gin.H{
			"trade_no":   order.TradeNo,
			"wallet":     order.WalletAddress,
			"amount":     order.ExpectedAmount,
			"chain":      order.Chain,
			"token":      order.Token,
			"expired_at": order.ExpiredAt,
		},
	})
}

// SubmitCryptoTxHash handles POST /api/dist/topup/crypto/submit
func SubmitCryptoTxHash(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "请先登录"})
		return
	}

	var req SubmitCryptoTxHashRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "参数错误: " + err.Error()})
		return
	}

	tradeNo := strings.TrimSpace(req.TradeNo)
	txHash := strings.TrimSpace(req.TxHash)

	if tradeNo == "" || txHash == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "订单号与交易哈希均不能为空"})
		return
	}

	order, err := model.GetCryptoTransactionByTradeNo(tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "订单不存在"})
		return
	}

	if order.UserId != userId {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "无权操作该订单"})
		return
	}

	if order.Status == model.CryptoStatusSuccess {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "success",
			"data": gin.H{
				"status":        "success",
				"actual_amount": order.ActualAmount,
				"trade_no":      order.TradeNo,
			},
		})
		return
	}

	// Trigger verification and settlement
	success, msg, actualAmount, err := service.VerifyAndSettleCryptoTx(c.Request.Context(), tradeNo, txHash)
	if err != nil || !success {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "error",
			"data":    "链上验证失败: " + msg,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "success",
		"data": gin.H{
			"status":        "success",
			"actual_amount": actualAmount,
			"trade_no":      tradeNo,
		},
	})
}

// GetCryptoOrderStatus handles GET /api/dist/topup/crypto/status
func GetCryptoOrderStatus(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Query("trade_no"))
	if tradeNo == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "缺少 trade_no 参数"})
		return
	}

	order, err := model.GetCryptoTransactionByTradeNo(tradeNo)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "error", "data": "订单不存在"})
		return
	}

	// If pending/processing and expired, mark as expired
	if (order.Status == model.CryptoStatusPending || order.Status == model.CryptoStatusProcessing) &&
		order.ExpiredAt > 0 && time.Now().Unix() > order.ExpiredAt {
		_ = model.ExpireCryptoTransaction(tradeNo, "订单超时未完成")
		order.Status = model.CryptoStatusExpired
		order.FailReason = "订单超时未完成"
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "success",
		"data": gin.H{
			"trade_no":      order.TradeNo,
			"status":        order.Status,
			"amount":        order.ExpectedAmount,
			"actual_amount": order.ActualAmount,
			"quota_amount":  order.QuotaAmount,
			"chain":         order.Chain,
			"token":         order.Token,
			"tx_hash":       order.TxHash,
			"fail_reason":   order.FailReason,
			"wallet":        order.WalletAddress,
		},
	})
}
