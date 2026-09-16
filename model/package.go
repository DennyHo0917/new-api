package model

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Package struct {
	Id               int     `json:"id" gorm:"primaryKey"`
	Name             string  `json:"name" gorm:"type:varchar(64);not null"`
	Description      string  `json:"description" gorm:"type:varchar(255);default:''"`
	Price            float64 `json:"price" gorm:"type:decimal(10,2);not null;default:0"`
	OriginalPrice    float64 `json:"original_price" gorm:"type:decimal(10,2);default:0"`
	Duration         int     `json:"duration" gorm:"type:int;not null;default:30"` // in days
	QuotaAmount      int64   `json:"quota_amount" gorm:"type:bigint;not null;default:0"`
	QuotaResetPeriod string  `json:"quota_reset_period" gorm:"type:varchar(32);default:'never'"` // never, daily, weekly, monthly
	Models           string  `json:"models" gorm:"type:text"`                                    // JSON or comma-separated
	Enabled          bool    `json:"enabled" gorm:"index;default:true"`
	SortOrder        int     `json:"sort_order" gorm:"default:0"`
	CreatedAt        int64   `json:"created_at" gorm:"autoCreateTime:milli"`
	UpdatedAt        int64   `json:"updated_at" gorm:"autoUpdateTime:milli"`
}

type UserPackageSubscription struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	UserId      int    `json:"user_id" gorm:"index;not null"`
	PackageId   int    `json:"package_id" gorm:"index;not null"`
	PackageName string `json:"package_name" gorm:"type:varchar(64);not null"`
	StartTime   int64  `json:"start_time" gorm:"not null"`
	EndTime     int64  `json:"end_time" gorm:"not null;index"`
	Status      string `json:"status" gorm:"type:varchar(32);not null;default:'active'"` // active, expired, cancelled
	QuotaAmount int64  `json:"quota_amount" gorm:"type:bigint;not null;default:0"`
	CreatedAt   int64  `json:"created_at" gorm:"autoCreateTime:milli"`
	UpdatedAt   int64  `json:"updated_at" gorm:"autoUpdateTime:milli"`
}

func GetAllEnabledPackages() ([]*Package, error) {
	var packages []*Package
	err := DB.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&packages).Error
	return packages, err
}

func GetPackageById(id int) (*Package, error) {
	if id <= 0 {
		return nil, errors.New("invalid package id")
	}
	var pkg Package
	err := DB.First(&pkg, id).Error
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

func GetUserActiveSubscriptions(userId int) ([]*UserPackageSubscription, error) {
	var subs []*UserPackageSubscription
	now := time.Now().Unix()
	err := DB.Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Order("id desc").Find(&subs).Error
	return subs, err
}

func CreatePackageSubscription(userId int, pkg *Package) (*UserPackageSubscription, error) {
	if pkg == nil || userId <= 0 {
		return nil, errors.New("invalid user or package")
	}

	now := time.Now()
	startTime := now.Unix()
	durationDays := pkg.Duration
	if durationDays <= 0 {
		durationDays = 30
	}
	endTime := now.AddDate(0, 0, durationDays).Unix()

	sub := &UserPackageSubscription{
		UserId:      userId,
		PackageId:   pkg.Id,
		PackageName: pkg.Name,
		StartTime:   startTime,
		EndTime:     endTime,
		Status:      "active",
		QuotaAmount: pkg.QuotaAmount,
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userId).Error; err != nil {
			return fmt.Errorf("user not found: %w", err)
		}

		if err := tx.Create(sub).Error; err != nil {
			return fmt.Errorf("failed to create subscription: %w", err)
		}

		if pkg.QuotaAmount > 0 {
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota + ?", pkg.QuotaAmount)).Error; err != nil {
				return fmt.Errorf("failed to increment quota: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return sub, nil
}
