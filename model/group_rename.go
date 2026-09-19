package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func replaceRoutingGroup(groups []string, oldName, newName string) ([]string, bool) {
	changed := false
	seen := make(map[string]bool, len(groups))
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == oldName {
			group = newName
			changed = true
		}
		if group != "" && !seen[group] {
			seen[group] = true
			result = append(result, group)
		}
	}
	return result, changed
}

// RenameRoutingGroup updates every persisted routing reference together so a
// renamed group keeps its channels, per-model assignments and existing keys.
func RenameRoutingGroup(oldName, newName string, optionValues map[string]string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" || oldName == "default" || oldName == newName {
		return errors.New("invalid group rename")
	}
	var updatedChannels []Channel
	var updatedTokens []Token
	var affectedUsers []int
	var affectedPlans []int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channels []Channel
		if err := tx.Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			groups, groupChanged := replaceRoutingGroup(channel.GetGroups(), oldName, newName)
			settings := channel.GetOtherSettings()
			settingsChanged := false
			for modelName, modelGroups := range settings.ModelGroups {
				renamed, changed := replaceRoutingGroup(modelGroups, oldName, newName)
				if changed {
					settings.ModelGroups[modelName] = renamed
					settingsChanged = true
				}
			}
			if !groupChanged && !settingsChanged {
				continue
			}
			channel.Group = strings.Join(groups, ",")
			channel.SetOtherSettings(settings)
			if err := channel.ValidateSettings(); err != nil {
				return err
			}
			if err := tx.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
				"group": channel.Group, "settings": channel.OtherSettings,
			}).Error; err != nil {
				return err
			}
			if err := channel.UpdateAbilities(tx); err != nil {
				return err
			}
			updatedChannels = append(updatedChannels, channel)
		}

		if err := tx.Where(`"group" = ? OR auto_groups <> ''`, oldName).Find(&updatedTokens).Error; err != nil {
			return err
		}
		for index := range updatedTokens {
			token := &updatedTokens[index]
			changed := false
			if token.Group == oldName {
				token.Group = newName
				changed = true
			}
			autoGroups, err := token.GetAutoGroups()
			if err != nil {
				return err
			}
			if renamed, autoChanged := replaceRoutingGroup(autoGroups, oldName, newName); autoChanged {
				if err := token.SetAutoGroups(renamed); err != nil {
					return err
				}
				changed = true
			}
			if changed {
				if err := tx.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{
					"group": token.Group, "auto_groups": token.AutoGroups,
				}).Error; err != nil {
					return err
				}
			}
		}

		if err := tx.Model(&User{}).Where(`"group" = ?`, oldName).Pluck("id", &affectedUsers).Error; err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where(`"group" = ?`, oldName).Update("group", newName).Error; err != nil {
			return err
		}
		if err := tx.Model(&SubscriptionPlan{}).Where("upgrade_group = ? OR downgrade_group = ?", oldName, oldName).Pluck("id", &affectedPlans).Error; err != nil {
			return err
		}
		for _, field := range []string{"upgrade_group", "downgrade_group"} {
			if err := tx.Model(&SubscriptionPlan{}).Where(field+" = ?", oldName).Update(field, newName).Error; err != nil {
				return err
			}
		}
		for _, field := range []string{"upgrade_group", "downgrade_group", "prev_user_group"} {
			if err := tx.Model(&UserSubscription{}).Where(field+" = ?", oldName).Update(field, newName).Error; err != nil {
				return err
			}
		}
		for key, value := range optionValues {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			option.Value = value
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, channel := range updatedChannels {
		CacheUpdateChannel(&channel)
	}
	if err := invalidateTokensCache(updatedTokens); err != nil {
		common.SysLog("failed to invalidate token cache after group rename: " + err.Error())
	}
	for _, userID := range affectedUsers {
		if err := invalidateUserCache(userID); err != nil {
			common.SysLog("failed to invalidate user cache after group rename: " + err.Error())
		}
	}
	for _, planID := range affectedPlans {
		InvalidateSubscriptionPlanCache(planID)
	}
	for key, value := range optionValues {
		if err := updateOptionMap(key, value); err != nil {
			return err
		}
	}
	InvalidatePricingCache()
	return nil
}
