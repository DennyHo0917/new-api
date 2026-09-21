package service

import (
	"errors"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func GetChannelConstraints(c *gin.Context) *dto.ChannelConstraints {
	if c == nil {
		return &dto.ChannelConstraints{}
	}
	if existing, ok := common.GetContextKeyType[*dto.ChannelConstraints](c, constant.ContextKeyChannelConstraints); ok && existing != nil {
		return existing
	}
	constraints := &dto.ChannelConstraints{}
	common.SetContextKey(c, constant.ContextKeyChannelConstraints, constraints)
	return constraints
}

func AppendTaskPluginIdentityFilter(c *gin.Context, pluginKey string) {
	if c == nil {
		return
	}
	channelTypes, pluginKeys := pinnedTaskPluginIdentities(c, pluginKey)
	GetChannelConstraints(c).AddFilter(dto.ChannelFilter{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          pluginKey,
		TaskPluginChannelTypes: channelTypes,
		TaskPluginKeys:         pluginKeys,
	})
}

type RetryParam struct {
	Ctx         *gin.Context
	TokenGroup  string
	ModelName   string
	RequestPath string
	Retry       *int
}

// GetRequestAutoGroupsByPrice returns the token's available Auto groups with the
// lowest effective group price for the requested model first.
func GetRequestAutoGroupsByPrice(c *gin.Context, userGroup, modelName string) []string {
	groups := GetRequestAutoGroups(c, userGroup)
	prices := make(map[string]float64)
	routingName := ratio_setting.RoutingMatchModelName(modelName)
	pricingList := model.GetPricing()
	var matched *model.Pricing
	for index := range pricingList {
		if pricingList[index].ModelName == modelName {
			matched = &pricingList[index]
			break
		}
	}
	if matched == nil && routingName != modelName {
		for index := range pricingList {
			if pricingList[index].ModelName == routingName {
				matched = &pricingList[index]
				break
			}
		}
	}
	if matched != nil {
		for group, multipliers := range matched.ChannelMultipliers {
			fallback := GetUserGroupRatio(userGroup, group)
			minimum := fallback
			for index, multiplier := range multipliers {
				value := fallback
				if multiplier != nil {
					value = *multiplier
				}
				if index == 0 || value < minimum {
					minimum = value
				}
			}
			prices[group] = minimum
		}
	}

	ordered := append([]string(nil), groups...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftPriced := prices[ordered[i]]
		right, rightPriced := prices[ordered[j]]
		if leftPriced != rightPriced {
			return leftPriced
		}
		return leftPriced && left < right
	})
	return ordered
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.Retry == nil {
		p.Retry = new(int)
	}
	*p.Retry++
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// Auto keys stay in the cheapest eligible standard group for every retry.
func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	filters := GetChannelConstraints(param.Ctx).Filters

	if param.TokenGroup == "auto" {
		autoGroups := GetRequestAutoGroupsByPrice(param.Ctx, userGroup, param.ModelName)
		if len(autoGroups) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
		}

		autoGroup := autoGroups[0]
		logger.LogDebug(param.Ctx, "Auto selecting cheapest group: %s, retry: %d", autoGroup, param.GetRetry())
		channel, _ = model.GetRandomSatisfiedChannel(autoGroup, param.ModelName, param.GetRetry(), filters)
		if channel != nil {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)
		}
	} else {
		channel, err = model.GetRandomSatisfiedChannel(
			param.TokenGroup,
			param.ModelName,
			param.GetRetry(),
			filters,
		)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

func pinnedTaskPluginIdentities(c *gin.Context, expected string) ([]int, []string) {
	if c == nil || expected == "" {
		return nil, nil
	}
	if value, exists := c.Get(jsplugin.ContextKeyPinnedEndpoint); exists {
		pinned, ok := value.(jsplugin.PinnedEndpoint)
		if ok && pinned.Generation != nil && len(pinned.Candidates) > 1 {
			expectedFound := false
			channelTypes := make([]int, 0, len(pinned.Candidates))
			pluginKeys := make([]string, 0, len(pinned.Candidates))
			seen := make(map[int]struct{}, len(pinned.Candidates))
			for _, candidate := range pinned.Candidates {
				if candidate.Plugin == nil {
					continue
				}
				if candidate.Plugin.Meta.Key == expected {
					expectedFound = true
				}
				pluginKeys = append(pluginKeys, candidate.Plugin.Meta.Key)
				for _, channelType := range candidate.Plugin.Meta.ChannelTypes {
					if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
						continue
					}
					if _, duplicate := seen[channelType]; duplicate {
						continue
					}
					if plugin, indexed := pinned.Generation.GetByChannelType(channelType); indexed && plugin == candidate.Plugin {
						seen[channelType] = struct{}{}
						channelTypes = append(channelTypes, channelType)
					}
				}
			}
			if expectedFound {
				return channelTypes, pluginKeys
			}
		}
	}
	value, exists := c.Get(jsplugin.ContextKeyPinnedPlugin)
	pinned, ok := value.(jsplugin.PinnedPlugin)
	if !exists || !ok || pinned.Generation == nil || pinned.Plugin == nil || pinned.Plugin.Meta.Key != expected {
		return nil, nil
	}
	channelTypes := make([]int, 0, len(pinned.Plugin.Meta.ChannelTypes))
	for _, channelType := range pinned.Plugin.Meta.ChannelTypes {
		if channelType == 0 || channelType == constant.ChannelTypeTaskPlugin {
			continue
		}
		channelTypes = append(channelTypes, channelType)
	}
	return channelTypes, []string{expected}
}
