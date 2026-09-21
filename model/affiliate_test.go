package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type affiliateEarningMigrationV1 struct {
	Id        int    `gorm:"primaryKey"`
	InviterId int    `gorm:"index;not null"`
	InviteeId int    `gorm:"index;not null"`
	TradeNo   string `gorm:"type:varchar(255);uniqueIndex;not null"`
}

func enableAffiliatePaymentsForTest(t *testing.T) {
	t.Helper()
	payment := operation_setting.GetPaymentSetting()
	previous := *payment
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() { *payment = previous })
}

func createAffiliateTestUser(t *testing.T, username string, inviterId, affCount int) User {
	t.Helper()
	user := User{
		Username:    username,
		Password:    "unused-password-hash",
		DisplayName: username,
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AffCode:     username + "-aff",
		InviterId:   inviterId,
		AffCount:    affCount,
		AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func TestAffiliateCommissionRates(t *testing.T) {
	tests := []struct {
		invites int
		wantBps int
	}{
		{invites: 0, wantBps: 500},
		{invites: 19, wantBps: 500},
		{invites: 20, wantBps: 700},
		{invites: 49, wantBps: 700},
		{invites: 50, wantBps: 1000},
		{invites: 99, wantBps: 1000},
		{invites: 100, wantBps: 1500},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_invites", tt.invites), func(t *testing.T) {
			assert.Equal(t, tt.wantBps, AffiliateCommissionRateBps(tt.invites))
		})
	}
}

func TestAffiliateEarningMigration(t *testing.T) {
	tests := []struct {
		name      string
		dialector func() gorm.Dialector
	}{
		{name: "sqlite", dialector: func() gorm.Dialector {
			return sqlite.Open(filepath.Join(t.TempDir(), "affiliate-migration.db"))
		}},
		{name: "mysql", dialector: func() gorm.Dialector {
			dsn := os.Getenv("TEST_MYSQL_DSN")
			if dsn == "" {
				return nil
			}
			return mysql.Open(dsn)
		}},
		{name: "postgres", dialector: func() gorm.Dialector {
			dsn := os.Getenv("TEST_POSTGRES_DSN")
			if dsn == "" {
				return nil
			}
			return postgres.Open(dsn)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialector := tt.dialector()
			if dialector == nil {
				t.Skip("test database DSN is not configured")
			}
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			const table = "affiliate_earnings_migration_test"
			const payoutTable = "affiliate_payouts_migration_test"
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(table)
				_ = db.Migrator().DropTable(payoutTable)
			})

			require.NoError(t, db.Table(table).AutoMigrate(&affiliateEarningMigrationV1{}))
			require.NoError(t, db.Table(table).Create(&affiliateEarningMigrationV1{
				Id: 1, InviterId: 11, InviteeId: 12, TradeNo: "migration-existing-order",
			}).Error)
			require.NoError(t, db.Table(table).AutoMigrate(&AffiliateEarning{}))
			require.NoError(t, db.Table(table).AutoMigrate(&AffiliateEarning{}))
			assert.True(t, db.Table(table).Migrator().HasColumn(&AffiliateEarning{}, "commission_quota"))

			var saved affiliateEarningMigrationV1
			require.NoError(t, db.Table(table).First(&saved, 1).Error)
			assert.Equal(t, "migration-existing-order", saved.TradeNo)
			duplicate := affiliateEarningMigrationV1{InviterId: 21, InviteeId: 22, TradeNo: saved.TradeNo}
			assert.Error(t, db.Table(table).Create(&duplicate).Error)

			require.NoError(t, db.Table(payoutTable).AutoMigrate(&AffiliatePayout{}))
			require.NoError(t, db.Table(payoutTable).AutoMigrate(&AffiliatePayout{}))
			payout := AffiliatePayout{
				UserId: 11, Quota: 25_000, PaymentMethod: "bank", Status: AffiliatePayoutStatusPending,
				CreatedTime: 1, UpdatedTime: 1,
			}
			require.NoError(t, db.Table(payoutTable).Create(&payout).Error)
			var savedPayout AffiliatePayout
			require.NoError(t, db.Table(payoutTable).First(&savedPayout, payout.Id).Error)
			assert.Equal(t, payout.PaymentMethod, savedPayout.PaymentMethod)
			assert.Equal(t, payout.Quota, savedPayout.Quota)
		})
	}
}

func TestPaidTopUpCreditsAffiliateExactlyOnce(t *testing.T) {
	truncateTables(t)
	enableAffiliatePaymentsForTest(t)

	inviter := createAffiliateTestUser(t, "affiliate-inviter", 0, 20)
	overrideRateBps := 825
	inviter.AffCommissionRateBps = &overrideRateBps
	require.NoError(t, DB.Model(&inviter).Update("aff_commission_rate_bps", overrideRateBps).Error)
	invitee := createAffiliateTestUser(t, "affiliate-invitee", inviter.Id, 0)
	topUp := TopUp{
		UserId:          invitee.Id,
		Amount:          10,
		Money:           10,
		TradeNo:         "AFFILIATE-EPAY-001",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      common.GetTimestamp(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(&topUp).Error)

	const callers = 2
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			_, err := RechargeEpay(topUp.TradeNo, "alipay", "127.0.0.1")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.NoError(t, DB.First(&inviter, inviter.Id).Error)
	require.NoError(t, DB.First(&invitee, invitee.Id).Error)
	assert.Equal(t, 5_000_000, invitee.Quota)
	assert.Equal(t, 412_500, inviter.AffQuota)
	assert.Equal(t, 412_500, inviter.AffHistoryQuota)

	var earnings []AffiliateEarning
	require.NoError(t, DB.Find(&earnings).Error)
	require.Len(t, earnings, 1)
	assert.Equal(t, inviter.Id, earnings[0].InviterId)
	assert.Equal(t, invitee.Id, earnings[0].InviteeId)
	assert.Equal(t, 825, earnings[0].CommissionRateBps)
	assert.Equal(t, int64(412_500), earnings[0].CommissionQuota)

	items, total, err := GetAffiliateEarnings(inviter.Id, &common.PageInfo{Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, "affiliate-invitee", items[0].Username)
	assert.Equal(t, 0.0825, items[0].CommissionRate)
}

func TestPaidTopUpWithoutInviterHasNoAffiliateEarning(t *testing.T) {
	truncateTables(t)
	enableAffiliatePaymentsForTest(t)

	user := createAffiliateTestUser(t, "affiliate-direct-user", 0, 0)
	topUp := TopUp{
		UserId:          user.Id,
		Amount:          10,
		Money:           10,
		TradeNo:         "AFFILIATE-EPAY-002",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      common.GetTimestamp(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(&topUp).Error)
	_, err := RechargeEpay(topUp.TradeNo, "alipay", "127.0.0.1")
	require.NoError(t, err)

	var count int64
	require.NoError(t, DB.Model(&AffiliateEarning{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCreateAffiliatePayoutReservesBalance(t *testing.T) {
	truncateTables(t)
	user := createAffiliateTestUser(t, "affiliate-payout-user", 0, 0)
	user.AffQuota = 1_000_000
	require.NoError(t, DB.Save(&user).Error)

	payout, err := CreateAffiliatePayout(user.Id, 350_000, "alipay", "account-123")
	require.NoError(t, err)
	require.NotNil(t, payout)
	assert.Equal(t, user.Id, payout.UserId)
	assert.Equal(t, 350_000, payout.Quota)
	assert.Equal(t, AffiliatePayoutStatusPending, payout.Status)

	var reloaded User
	require.NoError(t, DB.First(&reloaded, user.Id).Error)
	assert.Equal(t, 650_000, reloaded.AffQuota)
	items, total, err := GetAffiliatePayouts(user.Id, &common.PageInfo{Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, 350_000, items[0].Quota)
	assert.Equal(t, "alipay", items[0].PaymentMethod)
}

func TestCreateAffiliatePayoutRejectsInsufficientBalanceAndSerializesConcurrentRequests(t *testing.T) {
	truncateTables(t)
	user := createAffiliateTestUser(t, "affiliate-payout-concurrent", 0, 0)
	user.AffQuota = 500_000
	require.NoError(t, DB.Save(&user).Error)

	const callers = 2
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			_, err := CreateAffiliatePayout(user.Id, 400_000, "bank", "")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, ErrAffiliatePayoutInsufficient)
	}
	assert.Equal(t, 1, successes)
	var reloaded User
	require.NoError(t, DB.First(&reloaded, user.Id).Error)
	assert.Equal(t, 100_000, reloaded.AffQuota)
	var count int64
	require.NoError(t, DB.Model(&AffiliatePayout{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestInvitationCountDoesNotDependOnSignupBonus(t *testing.T) {
	truncateTables(t)
	payment := operation_setting.GetPaymentSetting()
	previousPayment := *payment
	previousInviterQuota := common.QuotaForInviter
	payment.ComplianceConfirmed = false
	common.QuotaForInviter = 0
	t.Cleanup(func() {
		*payment = previousPayment
		common.QuotaForInviter = previousInviterQuota
	})

	inviter := createAffiliateTestUser(t, "affiliate-count-inviter", 0, 0)
	invitee := User{
		Username:    "affiliate-count-invitee",
		Password:    "valid-password",
		DisplayName: "affiliate-count-invitee",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		InviterId:   inviter.Id,
	}
	require.NoError(t, invitee.Insert(inviter.Id))
	require.NoError(t, DB.First(&inviter, inviter.Id).Error)
	assert.Equal(t, 1, inviter.AffCount)
	assert.Zero(t, inviter.AffQuota)
}
