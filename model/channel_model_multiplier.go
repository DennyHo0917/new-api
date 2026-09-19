package model

import (
	"errors"
	"math"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ChannelModelMultiplier struct {
	ChannelID   int      `json:"channel_id"`
	ChannelName string   `json:"channel_name"`
	Groups      []string `json:"groups"`
	Multiplier  *float64 `json:"multiplier"`
}

func GetChannelModelMultipliers(modelName string) ([]ChannelModelMultiplier, error) {
	var abilities []struct {
		ChannelID int
		Group     string
	}
	if err := DB.Model(&Ability{}).Select("channel_id, \"group\"").Where("model = ?", modelName).Find(&abilities).Error; err != nil {
		return nil, err
	}
	byChannel := make(map[int]*ChannelModelMultiplier)
	for _, ability := range abilities {
		entry := byChannel[ability.ChannelID]
		if entry == nil {
			var channel Channel
			if err := DB.Select("id", "name", "settings").First(&channel, "id = ?", ability.ChannelID).Error; err != nil {
				return nil, err
			}
			entry = &ChannelModelMultiplier{ChannelID: channel.Id, ChannelName: channel.Name}
			if multiplier, ok := channel.GetOtherSettings().ModelMultipliers[modelName]; ok {
				entry.Multiplier = common.GetPointer(multiplier)
			}
			byChannel[channel.Id] = entry
		}
		if !common.StringsContains(entry.Groups, ability.Group) {
			entry.Groups = append(entry.Groups, ability.Group)
		}
	}
	result := make([]ChannelModelMultiplier, 0, len(byChannel))
	for _, entry := range byChannel {
		sort.Strings(entry.Groups)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ChannelID < result[j].ChannelID })
	return result, nil
}

func SetChannelModelMultiplier(channelID int, modelName string, multiplier *float64) error {
	if modelName == "" || multiplier != nil && (*multiplier < 0 || math.IsNaN(*multiplier) || math.IsInf(*multiplier, 0)) {
		return errors.New("invalid channel model multiplier")
	}
	var updated Channel
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&updated, "id = ?", channelID).Error; err != nil {
			return err
		}
		if !common.StringsContains(updated.GetModels(), modelName) {
			return errors.New("channel does not provide this model")
		}
		settings := updated.GetOtherSettings()
		if settings.ModelMultipliers == nil {
			settings.ModelMultipliers = make(map[string]float64)
		}
		if multiplier == nil {
			delete(settings.ModelMultipliers, modelName)
		} else {
			settings.ModelMultipliers[modelName] = *multiplier
		}
		updated.SetOtherSettings(settings)
		return tx.Model(&Channel{}).Where("id = ?", channelID).Update("settings", updated.OtherSettings).Error
	})
	if err != nil {
		return err
	}
	CacheUpdateChannel(&updated)
	InvalidatePricingCache()
	return nil
}
