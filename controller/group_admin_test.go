package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
