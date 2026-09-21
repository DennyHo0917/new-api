package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRequestAutoGroupsTest(t *testing.T) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["gpt enterprise","gpt standard","claude standard"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","gpt enterprise":"Enterprise","gpt standard":"GPT","claude standard":"Claude"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"gpt enterprise":1,"gpt standard":1,"claude standard":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
}

func newRequestAutoGroupsContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestGetRequestAutoGroupsInheritedListIsNotLimited(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"gpt standard", "claude standard"}, groups)
}

func TestGetRequestAutoGroupsInheritsEveryPricedStandardGroup(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["gpt standard"]`))
	ctx := newRequestAutoGroupsContext()

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"gpt standard", "claude standard"}, groups)
}

func TestGetRequestAutoGroupsFiltersBeforeApplyingCurrentLimit(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"revoked", "gpt enterprise", "gpt standard", "claude standard"})
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"gpt standard", "claude standard"}, groups)
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("1"))
	assert.Equal(t, []string{"gpt standard"}, GetRequestAutoGroups(ctx, "default"))
}

func TestGetRequestAutoGroupsDoesNotFallBackAfterPermissionChange(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"gpt standard"})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Empty(t, groups)
}
