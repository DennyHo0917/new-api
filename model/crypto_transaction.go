package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type CryptoTransactionStatus string

const (
	CryptoStatusPending    CryptoTransactionStatus = "pending"
	CryptoStatusProcessing CryptoTransactionStatus = "processing"
	CryptoStatusSuccess    CryptoTransactionStatus = "success"
	CryptoStatusFailed     CryptoTransactionStatus = "failed"
	CryptoStatusExpired    CryptoTransactionStatus = "expired"
)

type CryptoTransaction struct {
	Id             int                     `json:"id" gorm:"primaryKey"`
	TradeNo        string                  `json:"trade_no" gorm:"type:varchar(64);uniqueIndex;not null"`
	UserId         int                     `json:"user_id" gorm:"index;not null"`
	Chain          string                  `json:"chain" gorm:"type:varchar(32);not null"`
	Token          string                  `json:"token" gorm:"type:varchar(16);not null"`
	WalletAddress  string                  `json:"wallet_address" gorm:"type:varchar(128);not null"`
	ExpectedAmount float64                 `json:"expected_amount" gorm:"type:decimal(16,6);not null;default:0"`
	ActualAmount   float64                 `json:"actual_amount" gorm:"type:decimal(16,6);not null;default:0"`
	QuotaAmount    int64                   `json:"quota_amount" gorm:"type:bigint;not null;default:0"`
	TxHash         *string                 `json:"tx_hash" gorm:"type:varchar(128);uniqueIndex"`
	FromAddress    string                  `json:"from_address" gorm:"type:varchar(128);default:''"`
	Status         CryptoTransactionStatus `json:"status" gorm:"type:varchar(20);not null;default:'pending';index"`
	FailReason     string                  `json:"fail_reason" gorm:"type:varchar(255);default:''"`
	ExpiredAt      int64                   `json:"expired_at" gorm:"index;not null"`
	CreatedAt      int64                   `json:"created_at" gorm:"not null"`
	UpdatedAt      int64                   `json:"updated_at" gorm:"not null"`
}

var (
	ErrCryptoOrderNotFound = errors.New("crypto transaction order not found")
	ErrCryptoTxHashReused  = errors.New("transaction hash has already been used")
	ErrCryptoOrderInvalid  = errors.New("crypto transaction order is not in valid status")
)

func CreateCryptoTransaction(txRecord *CryptoTransaction) error {
	if txRecord == nil {
		return errors.New("crypto transaction cannot be nil")
	}
	now := time.Now().Unix()
	txRecord.CreatedAt = now
	txRecord.UpdatedAt = now
	if txRecord.Status == "" {
		txRecord.Status = CryptoStatusPending
	}
	return DB.Create(txRecord).Error
}

func GetCryptoTransactionByTradeNo(tradeNo string) (*CryptoTransaction, error) {
	if tradeNo == "" {
		return nil, ErrCryptoOrderNotFound
	}
	var txRecord CryptoTransaction
	err := DB.Where("trade_no = ?", tradeNo).First(&txRecord).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCryptoOrderNotFound
		}
		return nil, err
	}
	return &txRecord, nil
}

func GetCryptoTransactionByTxHash(txHash string) (*CryptoTransaction, error) {
	cleanHash := strings.TrimSpace(strings.ToLower(txHash))
	if cleanHash == "" {
		return nil, errors.New("empty tx hash")
	}
	var txRecord CryptoTransaction
	err := DB.Where("LOWER(tx_hash) = ?", cleanHash).First(&txRecord).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &txRecord, nil
}

func SubmitCryptoTransactionTxHash(tradeNo string, txHash string) error {
	cleanHash := strings.TrimSpace(txHash)
	if cleanHash == "" {
		return errors.New("tx_hash cannot be empty")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing CryptoTransaction
		err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&existing).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCryptoOrderNotFound
			}
			return err
		}

		if existing.Status != CryptoStatusPending && existing.Status != CryptoStatusProcessing {
			return fmt.Errorf("%w: current status is %s", ErrCryptoOrderInvalid, existing.Status)
		}

		var count int64
		err = tx.Model(&CryptoTransaction{}).
			Where("LOWER(tx_hash) = ? AND trade_no != ?", strings.ToLower(cleanHash), tradeNo).
			Count(&count).Error
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrCryptoTxHashReused
		}

		now := time.Now().Unix()
		return tx.Model(&CryptoTransaction{}).
			Where("trade_no = ?", tradeNo).
			Updates(map[string]any{
				"tx_hash":    cleanHash,
				"status":     CryptoStatusProcessing,
				"updated_at": now,
			}).Error
	})
}

func CompleteCryptoTransaction(tradeNo string, actualAmount float64, quotaAmount int64, fromAddress string, txHash string) error {
	if actualAmount <= 0 {
		return errors.New("actual amount must be greater than zero")
	}
	if quotaAmount <= 0 {
		return errors.New("quota amount must be greater than zero")
	}

	cleanHash := strings.TrimSpace(txHash)
	now := time.Now().Unix()

	return DB.Transaction(func(tx *gorm.DB) error {
		var order CryptoTransaction
		err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCryptoOrderNotFound
			}
			return err
		}

		if order.Status == CryptoStatusSuccess {
			return nil
		}

		if cleanHash != "" {
			var count int64
			err = tx.Model(&CryptoTransaction{}).
				Where("LOWER(tx_hash) = ? AND trade_no != ?", strings.ToLower(cleanHash), tradeNo).
				Count(&count).Error
			if err != nil {
				return err
			}
			if count > 0 {
				return ErrCryptoTxHashReused
			}
		}

		updates := map[string]any{
			"status":        CryptoStatusSuccess,
			"actual_amount": actualAmount,
			"quota_amount":  quotaAmount,
			"from_address":  fromAddress,
			"updated_at":    now,
		}
		if cleanHash != "" {
			updates["tx_hash"] = cleanHash
		}

		if err := tx.Model(&CryptoTransaction{}).Where("id = ?", order.Id).Updates(updates).Error; err != nil {
			return err
		}

		topUpRecord := TopUp{
			UserId:          order.UserId,
			Amount:          quotaAmount,
			Money:           actualAmount,
			TradeNo:         order.TradeNo,
			PaymentMethod:   "crypto",
			PaymentProvider: fmt.Sprintf("crypto_%s_%s", strings.ToLower(order.Chain), strings.ToLower(order.Token)),
			CreateTime:      order.CreatedAt,
			CompleteTime:    now,
			Status:          "success",
		}
		if err := tx.Create(&topUpRecord).Error; err != nil {
			return fmt.Errorf("failed to record topup history: %w", err)
		}

		var user User
		if err := lockForUpdate(tx).First(&user, order.UserId).Error; err != nil {
			return fmt.Errorf("failed to lock user %d: %w", order.UserId, err)
		}

		result := tx.Model(&User{}).
			Where("id = ? AND quota <= ?", order.UserId, int64(common.MaxWalletQuota)-quotaAmount).
			Update("quota", gorm.Expr("quota + ?", quotaAmount))
		if result.Error != nil {
			return fmt.Errorf("failed to increment user quota: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errors.New("user quota capacity exceeded or user disappeared")
		}

		return nil
	})
}

func FailCryptoTransaction(tradeNo string, reason string) error {
	now := time.Now().Unix()
	return DB.Model(&CryptoTransaction{}).
		Where("trade_no = ? AND status IN ?", tradeNo, []CryptoTransactionStatus{CryptoStatusPending, CryptoStatusProcessing}).
		Updates(map[string]any{
			"status":      CryptoStatusFailed,
			"fail_reason": reason,
			"updated_at":  now,
		}).Error
}

func ExpirePendingCryptoTransactions(now int64) (int64, error) {
	res := DB.Model(&CryptoTransaction{}).
		Where("status IN ? AND expired_at <= ?", []CryptoTransactionStatus{CryptoStatusPending, CryptoStatusProcessing}, now).
		Updates(map[string]any{
			"status":     CryptoStatusExpired,
			"updated_at": now,
		})
	return res.RowsAffected, res.Error
}

func ExpireCryptoTransaction(tradeNo string, reason string) error {
	return DB.Model(&CryptoTransaction{}).
		Where("trade_no = ? AND status IN ?", tradeNo, []CryptoTransactionStatus{CryptoStatusPending, CryptoStatusProcessing}).
		Updates(map[string]any{
			"status":      CryptoStatusExpired,
			"fail_reason": reason,
			"updated_at":  time.Now().Unix(),
		}).Error
}

func GetUserCryptoTransactions(userId int, page int, pageSize int) ([]*CryptoTransaction, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var records []*CryptoTransaction
	var total int64

	db := DB.Model(&CryptoTransaction{}).Where("user_id = ?", userId)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := db.Order("id desc").Offset(offset).Limit(pageSize).Find(&records).Error
	return records, total, err
}
