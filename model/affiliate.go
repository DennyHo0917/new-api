package model

import (
	"errors"
	"strings"

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

const AffiliatePayoutStatusPending = "pending"

var (
	ErrAffiliatePayoutInvalid      = errors.New("invalid affiliate payout")
	ErrAffiliatePayoutInsufficient = errors.New("affiliate quota insufficient")
)

// AffiliatePayout records a withdrawal request after its quota is atomically
// reserved from the inviter wallet. The pending row is the handoff for the
// administrator's external payment process.
type AffiliatePayout struct {
	Id            int    `json:"id" gorm:"primaryKey"`
	UserId        int    `json:"user_id" gorm:"index;not null"`
	Quota         int    `json:"quota" gorm:"type:bigint;not null;default:0"`
	PaymentMethod string `json:"payment_method" gorm:"type:varchar(128);not null"`
	Remark        string `json:"remark" gorm:"type:text"`
	Status        string `json:"status" gorm:"type:varchar(20);not null;default:'pending';index"`
	CreatedTime   int64  `json:"created_time" gorm:"index;not null;default:0"`
	UpdatedTime   int64  `json:"updated_time" gorm:"not null;default:0"`
	ProcessedTime int64  `json:"processed_time" gorm:"not null;default:0"`
}

type AffiliatePayoutItem struct {
	Id            int     `json:"id"`
	Quota         int     `json:"quota"`
	Amount        float64 `json:"amount"`
	PaymentMethod string  `json:"payment_method"`
	Remark        string  `json:"remark"`
	Status        string  `json:"status"`
	CreatedTime   int64   `json:"created_time"`
	UpdatedTime   int64   `json:"updated_time"`
	ProcessedTime int64   `json:"processed_time"`
}

// CreateAffiliatePayout atomically reserves the requested affiliate quota and
// creates a pending payout record. A row lock plus a guarded update prevents
// concurrent requests from spending the same balance twice.
func CreateAffiliatePayout(userID, quota int, paymentMethod, remark string) (*AffiliatePayout, error) {
	paymentMethod = strings.TrimSpace(paymentMethod)
	remark = strings.TrimSpace(remark)
	if userID <= 0 || quota <= 0 || quota > common.MaxWalletQuota || paymentMethod == "" || len(paymentMethod) > 128 || len(remark) > 2000 {
		return nil, ErrAffiliatePayoutInvalid
	}

	var payout AffiliatePayout
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id", "aff_quota").First(&user, userID).Error; err != nil {
			return err
		}
		if user.AffQuota < quota {
			return ErrAffiliatePayoutInsufficient
		}
		result := tx.Model(&User{}).
			Where("id = ? AND aff_quota >= ?", userID, quota).
			Update("aff_quota", gorm.Expr("aff_quota - ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAffiliatePayoutInsufficient
		}

		now := common.GetTimestamp()
		payout = AffiliatePayout{
			UserId:        userID,
			Quota:         quota,
			PaymentMethod: paymentMethod,
			Remark:        remark,
			Status:        AffiliatePayoutStatusPending,
			CreatedTime:   now,
			UpdatedTime:   now,
		}
		return tx.Create(&payout).Error
	})
	if err != nil {
		return nil, err
	}
	return &payout, nil
}

func GetAffiliatePayouts(userID int, pageInfo *common.PageInfo) ([]AffiliatePayoutItem, int64, error) {
	if userID <= 0 {
		return nil, 0, ErrAffiliatePayoutInvalid
	}
	if pageInfo == nil {
		pageInfo = &common.PageInfo{Page: 1, PageSize: 20}
	}

	query := DB.Model(&AffiliatePayout{}).Where("user_id = ?", userID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []AffiliatePayout
	if err := query.Order("id DESC").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]AffiliatePayoutItem, 0, len(rows))
	for _, row := range rows {
		amount := 0.0
		if common.QuotaPerUnit > 0 {
			amount = float64(row.Quota) / common.QuotaPerUnit
		}
		items = append(items, AffiliatePayoutItem{
			Id:            row.Id,
			Quota:         row.Quota,
			Amount:        amount,
			PaymentMethod: row.PaymentMethod,
			Remark:        row.Remark,
			Status:        row.Status,
			CreatedTime:   row.CreatedTime,
			UpdatedTime:   row.UpdatedTime,
			ProcessedTime: row.ProcessedTime,
		})
	}
	return items, total, nil
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

func EffectiveAffiliateCommissionRateBps(user *User) int {
	if user != nil && user.AffCommissionRateBps != nil && *user.AffCommissionRateBps >= 0 && *user.AffCommissionRateBps <= 10000 {
		return *user.AffCommissionRateBps
	}
	if user == nil {
		return DefaultAffiliateCommissionBps
	}
	return AffiliateCommissionRateBps(user.AffCount)
}

func EffectiveAffiliateCommissionRate(user *User) float64 {
	return float64(EffectiveAffiliateCommissionRateBps(user)) / 10000
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
		Select("id", "aff_count", "aff_commission_rate_bps", "aff_quota", "aff_history").
		First(&inviter, invitee.InviterId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	rateBps := EffectiveAffiliateCommissionRateBps(&inviter)
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
