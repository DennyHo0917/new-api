package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRatiosPersistsValidatedValues(t *testing.T) {
	db := openTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.User{}, &model.AuditLog{}))
	require.NoError(t, db.Create(&model.User{Id: 7, Username: "group-admin", Role: common.RoleAdminUser}).Error)

	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	previousRatios := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
	})

	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/group/ratios", map[string]any{
		"ratios": map[string]float64{"default": 1, "partner": 0.75},
	}, 7)
	ctx.Set("role", common.RoleAdminUser)
	UpdateGroupRatios(ctx)
	response := decodeAPIResponse(t, recorder)
	require.True(t, response.Success, response.Message)

	var option model.Option
	require.NoError(t, db.Where("key = ?", "GroupRatio").First(&option).Error)
	assert.JSONEq(t, `{"default":1,"partner":0.75}`, option.Value)
	assert.Equal(t, float64(0.75), ratio_setting.GetGroupRatio("partner"))

	ctx, recorder = newAuthenticatedContext(t, http.MethodPut, "/api/group/ratios", map[string]any{
		"ratios": map[string]float64{"default": -1},
	}, 7)
	ctx.Set("role", common.RoleAdminUser)
	UpdateGroupRatios(ctx)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, float64(0.75), ratio_setting.GetGroupRatio("partner"))
}

func TestUpdateGroupDisplayOrderPersistsKnownGroups(t *testing.T) {
	db := openTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.User{}, &model.AuditLog{}))
	require.NoError(t, db.Create(&model.User{Id: 8, Username: "group-order-admin", Role: common.RoleAdminUser}).Error)

	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	previousRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"fast":0.8,"stable":1.2}`))
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
	})

	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/group/display-order", map[string]any{
		"order": []string{"stable", "unknown", "fast", "stable"},
	}, 8)
	ctx.Set("role", common.RoleAdminUser)
	UpdateGroupDisplayOrder(ctx)
	response := decodeAPIResponse(t, recorder)
	require.True(t, response.Success, response.Message)
	var order []string
	require.NoError(t, common.Unmarshal(response.Data, &order))
	assert.Equal(t, []string{"stable", "fast", "default"}, order)

	var option model.Option
	require.NoError(t, db.Where("key = ?", groupDisplayOrderOption).First(&option).Error)
	assert.JSONEq(t, `["stable","unknown","fast","stable"]`, option.Value)
}

func TestRenameGroupPreservesChannelModelAssignments(t *testing.T) {
	db := openTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Option{}, &model.User{}, &model.AuditLog{}, &model.Channel{}, &model.Ability{},
		&model.Token{}, &model.SubscriptionPlan{}, &model.UserSubscription{},
	))
	require.NoError(t, db.Create(&model.User{Id: 9, Username: "group-rename-admin", Role: common.RoleAdminUser, AffCode: "rename-admin"}).Error)
	require.NoError(t, db.Create(&model.User{Id: 10, Username: "group-user", Group: "deepseek pro", AffCode: "rename-user"}).Error)

	previousOptions := common.OptionMap
	previousRatios := ratio_setting.GroupRatio2JSONString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousTopupRatios := common.TopupGroupRatio2JSONString()
	previousRateLimits := setting.ModelRequestRateLimitGroup2JSONString()
	common.OptionMap = map[string]string{}
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"deepseek pro":0.7}`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"默认分组","deepseek pro":"DeepSeek"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","deepseek pro"]`))
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"deepseek pro":1.1}`))
	require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(`{"deepseek pro":[20,10]}`))
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(previousTopupRatios))
		require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(previousRateLimits))
	})

	channel := model.Channel{Name: "deepseek", Key: "test-key", Models: "deepseek-chat", Group: "deepseek pro", Status: common.ChannelStatusEnabled}
	channel.SetOtherSettings(dto.ChannelOtherSettings{ModelGroups: map[string][]string{"deepseek-chat": {"deepseek pro"}}})
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, channel.UpdateAbilities(db))
	require.NoError(t, db.Create(&model.Token{UserId: 10, Key: "group-rename-token", Group: "auto", AutoGroups: `["deepseek pro"]`}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPlan{Title: "plan", UpgradeGroup: "deepseek pro", DowngradeGroup: "deepseek pro"}).Error)

	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/group/rename", map[string]any{
		"old_name": "deepseek pro", "new_name": "deepseek enterprise", "ratio": 0.75,
	}, 9)
	ctx.Set("role", common.RoleAdminUser)
	RenameGroup(ctx)
	response := decodeAPIResponse(t, recorder)
	require.True(t, response.Success, response.Message)

	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.Equal(t, "deepseek enterprise", channel.Group)
	assert.Equal(t, []string{"deepseek enterprise"}, channel.GetOtherSettings().ModelGroups["deepseek-chat"])
	var ability model.Ability
	require.NoError(t, db.Where("channel_id = ?", channel.Id).First(&ability).Error)
	assert.Equal(t, "deepseek enterprise", ability.Group)
	var token model.Token
	require.NoError(t, db.Where("key = ?", "group-rename-token").First(&token).Error)
	assert.JSONEq(t, `["deepseek enterprise"]`, token.AutoGroups)
	var user model.User
	require.NoError(t, db.First(&user, 10).Error)
	assert.Equal(t, "deepseek enterprise", user.Group)
	assert.Equal(t, float64(0.75), ratio_setting.GetGroupRatio("deepseek enterprise"))
	assert.False(t, ratio_setting.ContainsGroupRatio("deepseek pro"))
}
