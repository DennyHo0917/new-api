package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","basic standard":"Basic","premium standard":"Premium","premium enterprise":"Enterprise"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"basic standard":1,"premium standard":2,"premium enterprise":3}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string, multiplier *float64) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	otherSettings := ""
	if multiplier != nil {
		data, err := common.Marshal(relaykitdto.ChannelOtherSettings{ModelMultipliers: map[string]float64{modelName: *multiplier}})
		require.NoError(t, err)
		otherSettings = string(data)
	}
	require.NoError(t, db.Create(&model.Channel{
		Id:            id,
		Type:          constant.ChannelTypeOpenAI,
		Key:           fmt.Sprintf("key-%d", id),
		Status:        common.ChannelStatusEnabled,
		Name:          fmt.Sprintf("channel-%d", id),
		Weight:        &weight,
		Models:        modelName,
		Group:         group,
		Priority:      &priority,
		OtherSettings: otherSettings,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func TestCacheGetRandomSatisfiedChannelUsesCheapestAutoGroupOnEveryRetry(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "premium standard", modelName, nil)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "basic standard", modelName, nil)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"premium standard", "basic standard"})

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2102, first.Id)
	assert.Equal(t, "basic standard", selectedGroup)
	assert.Equal(t, "basic standard", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
	assert.Empty(t, setting.GetAutoGroups(), "the selection must not depend on the global Auto list")

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "basic standard", selectedGroup)
	assert.Equal(t, "basic standard", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}

func TestCacheGetRandomSatisfiedChannelUsesChannelModelMultiplier(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-multiplier-model"
	cheaper := 0.25
	createChannelSelectAutoGroupsChannel(t, db, 2201, "basic standard", modelName, nil)
	createChannelSelectAutoGroupsChannel(t, db, 2202, "premium standard", modelName, &cheaper)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"basic standard", "premium standard"})

	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:        ctx,
		TokenGroup: "auto",
		ModelName:  modelName,
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2202, channel.Id)
	assert.Equal(t, "premium standard", selectedGroup)
}

func TestCacheGetRandomSatisfiedChannelDoesNotFallBackToExpensiveGroup(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-strict-cheapest-model"
	cheaper := 0.25
	createChannelSelectAutoGroupsChannel(t, db, 2301, "basic standard", modelName, nil)
	createChannelSelectAutoGroupsChannel(t, db, 2302, "premium standard", modelName, &cheaper)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"basic standard", "premium standard"})
	require.Equal(t, []string{"premium standard", "basic standard"}, GetRequestAutoGroupsByPrice(ctx, "default", modelName))
	model.CacheUpdateChannelStatus(2302, common.ChannelStatusManuallyDisabled)

	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:        ctx,
		TokenGroup: "auto",
		ModelName:  modelName,
	})
	require.NoError(t, err)
	assert.Nil(t, channel)
	assert.Equal(t, "auto", selectedGroup)
}

func TestCacheGetRandomSatisfiedChannelAllowsExplicitEnterpriseGroup(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "explicit-enterprise-model"
	createChannelSelectAutoGroupsChannel(t, db, 2401, "premium enterprise", modelName, nil)
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:        ctx,
		TokenGroup: "premium enterprise",
		ModelName:  modelName,
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 2401, channel.Id)
	assert.Equal(t, "premium enterprise", selectedGroup)
}

func TestGetRequestAutoGroupsByPricePreservesOrderWithoutPricing(t *testing.T) {
	setupChannelSelectAutoGroupsTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"premium standard", "basic standard"})

	assert.Equal(t, []string{"premium standard", "basic standard"}, GetRequestAutoGroupsByPrice(ctx, "default", "missing-model"))
}
