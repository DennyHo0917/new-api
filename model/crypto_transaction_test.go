package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCryptoTransactionIsExpired(t *testing.T) {
	order := &CryptoTransaction{ExpiredAt: 1_000}
	assert.False(t, order.IsExpired(999))
	assert.True(t, order.IsExpired(1_000))
	assert.False(t, (&CryptoTransaction{}).IsExpired(1_000))
}

func TestCryptoTransactionLifecycle(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&User{}, &CryptoTransaction{}, &TopUp{}))

	// Clean up
	_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&CryptoTransaction{}).Error
	_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&TopUp{}).Error
	_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error

	testUser := User{
		Id:       9001,
		Username: "cryptotester",
		Quota:    1000,
	}
	require.NoError(t, DB.Create(&testUser).Error)

	now := time.Now().Unix()
	txOrder := &CryptoTransaction{
		TradeNo:        "CRYPTO-TEST-001",
		UserId:         testUser.Id,
		Chain:          "tron",
		Token:          "USDT",
		WalletAddress:  "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		ExpectedAmount: 10.0,
		ExpiredAt:      now + 1800,
	}

	// 1. Test Create
	require.NoError(t, CreateCryptoTransaction(txOrder))
	assert.Greater(t, txOrder.Id, 0)
	assert.Equal(t, CryptoStatusPending, txOrder.Status)

	// 2. Test Get by TradeNo
	fetched, err := GetCryptoTransactionByTradeNo("CRYPTO-TEST-001")
	require.NoError(t, err)
	assert.Equal(t, txOrder.Id, fetched.Id)
	assert.Equal(t, "tron", fetched.Chain)
	assert.Nil(t, fetched.TxHash)

	// 3. Test Submit TxHash
	sampleHash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	require.NoError(t, SubmitCryptoTransactionTxHash("CRYPTO-TEST-001", sampleHash))

	fetchedAfterSubmit, err := GetCryptoTransactionByTradeNo("CRYPTO-TEST-001")
	require.NoError(t, err)
	assert.Equal(t, CryptoStatusProcessing, fetchedAfterSubmit.Status)
	require.NotNil(t, fetchedAfterSubmit.TxHash)
	assert.Equal(t, sampleHash, *fetchedAfterSubmit.TxHash)

	// 4. Test Hash Uniqueness (Prevent Replay Attack)
	anotherOrder := &CryptoTransaction{
		TradeNo:        "CRYPTO-TEST-002",
		UserId:         testUser.Id,
		Chain:          "arb",
		Token:          "USDT",
		WalletAddress:  "0x1234567890123456789012345678901234567890",
		ExpectedAmount: 20.0,
		ExpiredAt:      now + 1800,
	}
	require.NoError(t, CreateCryptoTransaction(anotherOrder))
	err = SubmitCryptoTransactionTxHash("CRYPTO-TEST-002", sampleHash)
	assert.ErrorIs(t, err, ErrCryptoTxHashReused, "reusing existing tx_hash must fail")

	// 5. Test Complete Crypto Transaction
	// User had 1000 quota, 10 USDT should add e.g. 5000000 quota
	creditedQuota := int64(5000000)
	actualAmount := 9.95 // received 9.95 USDT after fees
	require.NoError(t, CompleteCryptoTransaction("CRYPTO-TEST-001", actualAmount, creditedQuota, "sender-wallet", sampleHash))

	// Verify order status
	completedOrder, err := GetCryptoTransactionByTradeNo("CRYPTO-TEST-001")
	require.NoError(t, err)
	assert.Equal(t, CryptoStatusSuccess, completedOrder.Status)
	assert.Equal(t, actualAmount, completedOrder.ActualAmount)
	assert.Equal(t, creditedQuota, completedOrder.QuotaAmount)
	assert.Equal(t, "sender-wallet", completedOrder.FromAddress)

	// Verify user quota was credited
	var updatedUser User
	require.NoError(t, DB.First(&updatedUser, testUser.Id).Error)
	assert.Equal(t, 1000+int(creditedQuota), updatedUser.Quota)

	// Verify TopUp history was recorded
	var topUp TopUp
	err = DB.Where("trade_no = ?", "CRYPTO-TEST-001").First(&topUp).Error
	require.NoError(t, err)
	assert.Equal(t, "crypto", topUp.PaymentMethod)
	assert.Equal(t, "crypto_tron_usdt", topUp.PaymentProvider)
	assert.Equal(t, creditedQuota, topUp.Amount)
	assert.Equal(t, actualAmount, topUp.Money)
	assert.Equal(t, "success", topUp.Status)

	// 6. Test Fail Order
	failOrder := &CryptoTransaction{
		TradeNo:        "CRYPTO-TEST-003",
		UserId:         testUser.Id,
		Chain:          "tron",
		Token:          "USDT",
		WalletAddress:  "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		ExpectedAmount: 50.0,
		ExpiredAt:      now + 1800,
	}
	require.NoError(t, CreateCryptoTransaction(failOrder))
	require.NoError(t, FailCryptoTransaction("CRYPTO-TEST-003", "transaction reverted on-chain"))
	failedFetch, err := GetCryptoTransactionByTradeNo("CRYPTO-TEST-003")
	require.NoError(t, err)
	assert.Equal(t, CryptoStatusFailed, failedFetch.Status)
	assert.Equal(t, "transaction reverted on-chain", failedFetch.FailReason)

	// 7. Test Expire Orders
	expiredOrder := &CryptoTransaction{
		TradeNo:        "CRYPTO-TEST-004",
		UserId:         testUser.Id,
		Chain:          "arb",
		Token:          "USDC",
		WalletAddress:  "0x1234567890123456789012345678901234567890",
		ExpectedAmount: 100.0,
		ExpiredAt:      now - 60, // already expired
	}
	require.NoError(t, CreateCryptoTransaction(expiredOrder))
	expiredCount, err := ExpirePendingCryptoTransactions(now)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, expiredCount, int64(1))
	expiredFetch, err := GetCryptoTransactionByTradeNo("CRYPTO-TEST-004")
	require.NoError(t, err)
	assert.Equal(t, CryptoStatusExpired, expiredFetch.Status)
}
