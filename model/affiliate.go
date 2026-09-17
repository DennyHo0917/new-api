package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	DefaultAffiliateCommissionBps = 500
	affiliateRate20InvitesBps     = 700
	affiliateRate50InvitesBps     = 1000
	affiliateRate100InvitesBps    = 1500
)

type AffiliateEarning struct {
	Id                int    `json:"id" gorm:"primaryKey"`
	InviterId         int    `json:"inviter_id" gorm:"index;not null"`
	InviteeId         int    `json:"invitee_id" gorm:"index;not null"`
	TopUpId           int    `json:"topup_id" gorm:"index;not null;default:0"`
	TradeNo           string `json:"trade_no" gorm:"type:varchar(255);uniqueIndex;not null"`
	PaymentProvider   string `json:"payment_provider" gorm:"type:varchar(50);not null;default:''"`
	TopUpQuota        int64  `json:"topup_quota" gorm:"type:bigint;not null;default:0"`
	CommissionRateBps int    `json:"commission_rate_bps" gorm:"type:int;not null;default:0"`
	CommissionQuota   int64  `json:"commission_quota" gorm:"type:bigint;not null;default:0"`
	CreatedTime       int64  `json:"created_time" gorm:"index;not null;default:0"`
}

type AffiliateEarningItem struct {
	Id              int     `json:"id"`
	UserId          int     `json:"user_id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"display_name"`
	ModelName       string  `json:"model_name"`
	TradeNo         string  `json:"trade_no"`
	TopUpQuota      int64   `json:"topup_quota"`
	CommissionRate  float64 `json:"commission_rate"`
	CommissionQuota int64   `json:"commission_quota"`
	CreatedTime     int64   `json:"created_time"`
}

func AffiliateCommissionRateBps(inviteCount int) int {
	switch {
	case inviteCount >= 100:
		return affiliateRate100InvitesBps
	case inviteCount >= 50:
		return affiliateRate50InvitesBps
	case inviteCount >= 20:
		return affiliateRate20InvitesBps
	default:
		return DefaultAffiliateCommissionBps
	}
}

func AffiliateCommissionRate(inviteCount int) float64 {
	return float64(AffiliateCommissionRateBps(inviteCount)) / 10000
}

func settlePaidTopUp(tx *gorm.DB, topUp *TopUp, creditedQuota int, updates map[string]any) error {
	if err := creditTopUpQuota(tx, topUp.UserId, creditedQuota, updates); err != nil {
		return err
	}
	return creditAffiliateCommission(tx, topUp, creditedQuota)
}

func creditAffiliateCommission(tx *gorm.DB, topUp *TopUp, creditedQuota int) error {
	if topUp == nil || creditedQuota <= 0 || !operation_setting.IsPaymentComplianceConfirmed() {
		return nil
	}

	var invitee User
	if err := tx.Select("id", "inviter_id").First(&invitee, topUp.UserId).Error; err != nil {
		return err
	}
	if invitee.InviterId <= 0 || invitee.InviterId == invitee.Id {
		return nil
	}

	var inviter User
	if err := lockForUpdate(tx).
		Select("id", "aff_count", "aff_quota", "aff_history").
		First(&inviter, invitee.InviterId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	rateBps := AffiliateCommissionRateBps(inviter.AffCount)
	commissionQuota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(int64(creditedQuota)).
			Mul(decimal.NewFromInt(int64(rateBps))).
			Div(decimal.NewFromInt(10000)),
	)
	if err != nil {
		return err
	}
	if commissionQuota <= 0 {
		return nil
	}

	earning := AffiliateEarning{
		InviterId:         inviter.Id,
		InviteeId:         invitee.Id,
		TopUpId:           topUp.Id,
		TradeNo:           topUp.TradeNo,
		PaymentProvider:   topUp.PaymentProvider,
		TopUpQuota:        int64(creditedQuota),
		CommissionRateBps: rateBps,
		CommissionQuota:   int64(commissionQuota),
		CreatedTime:       common.GetTimestamp(),
	}
	if err := tx.Create(&earning).Error; err != nil {
		return err
	}

	maxCurrentQuota := common.MaxWalletQuota - commissionQuota
	result := tx.Model(&User{}).
		Where("id = ? AND aff_quota <= ? AND aff_history <= ?", inviter.Id, maxCurrentQuota, maxCurrentQuota).
		Updates(map[string]any{
			"aff_quota":   gorm.Expr("aff_quota + ?", commissionQuota),
			"aff_history": gorm.Expr("aff_history + ?", commissionQuota),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWalletQuotaLimitExceeded
	}
	return nil
}

func GetAffiliateEarnings(inviterId int, pageInfo *common.PageInfo) ([]AffiliateEarningItem, int64, error) {
	if pageInfo == nil {
		pageInfo = &common.PageInfo{Page: 1, PageSize: 20}
	}

	query := DB.Model(&AffiliateEarning{}).Where("inviter_id = ?", inviterId)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []struct {
		AffiliateEarning
		Username    string `gorm:"column:username"`
		DisplayName string `gorm:"column:display_name"`
	}
	err := DB.Table("affiliate_earnings").
		Select("affiliate_earnings.*, users.username, users.display_name").
		Joins("LEFT JOIN users ON users.id = affiliate_earnings.invitee_id").
		Where("affiliate_earnings.inviter_id = ?", inviterId).
		Order("affiliate_earnings.id DESC").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	items := make([]AffiliateEarningItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, AffiliateEarningItem{
			Id:              row.Id,
			UserId:          row.InviteeId,
			Username:        row.Username,
			DisplayName:     row.DisplayName,
			ModelName:       row.PaymentProvider,
			TradeNo:         row.TradeNo,
			TopUpQuota:      row.TopUpQuota,
			CommissionRate:  float64(row.CommissionRateBps) / 10000,
			CommissionQuota: row.CommissionQuota,
			CreatedTime:     row.CreatedTime,
		})
	}
	return items, total, nil
}
