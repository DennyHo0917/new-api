package controller

import (
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

const groupDisplayOrderOption = "DistGroupOrder"

func getGroupDisplayOrder() []string {
	ratios := ratio_setting.GetGroupRatioCopy()
	common.OptionMapRWMutex.RLock()
	raw := common.Interface2String(common.OptionMap[groupDisplayOrderOption])
	common.OptionMapRWMutex.RUnlock()

	configured := make([]string, 0, len(ratios))
	_ = common.UnmarshalJsonStr(raw, &configured)
	order := make([]string, 0, len(ratios))
	seen := make(map[string]bool, len(ratios))
	for _, name := range configured {
		name = strings.TrimSpace(name)
		if _, exists := ratios[name]; !exists || seen[name] {
			continue
		}
		seen[name] = true
		order = append(order, name)
	}
	missing := make([]string, 0, len(ratios)-len(order))
	for name := range ratios {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return append(order, missing...)
}

func GetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

func GetGroupRatios(c *gin.Context) {
	common.ApiSuccess(c, ratio_setting.GetGroupRatioCopy())
}

func UpdateGroupRatios(c *gin.Context) {
	var request struct {
		Ratios map[string]float64 `json:"ratios"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Ratios == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid group ratios"})
		return
	}
	raw, err := common.Marshal(request.Ratios)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := ratio_setting.CheckGroupRatio(string(raw)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := model.UpdateOption("GroupRatio", string(raw)); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "group.ratios.update", map[string]any{"groups": request.Ratios})
	common.ApiSuccess(c, request.Ratios)
}

func GetGroupDisplayOrder(c *gin.Context) {
	common.ApiSuccess(c, getGroupDisplayOrder())
}

func UpdateGroupDisplayOrder(c *gin.Context) {
	var request struct {
		Order []string `json:"order"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Order == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid group display order"})
		return
	}
	raw, err := common.Marshal(request.Order)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.UpdateOption(groupDisplayOrderOption, string(raw)); err != nil {
		common.ApiError(c, err)
		return
	}
	order := getGroupDisplayOrder()
	recordManageAudit(c, "group.display_order.update", map[string]any{"order": order})
	common.ApiSuccess(c, order)
}

func RenameGroup(c *gin.Context) {
	var request struct {
		OldName string  `json:"old_name"`
		NewName string  `json:"new_name"`
		Ratio   float64 `json:"ratio"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid group rename"})
		return
	}
	request.OldName = strings.TrimSpace(request.OldName)
	request.NewName = strings.TrimSpace(request.NewName)
	if request.OldName == "" || request.NewName == "" || request.OldName == "default" || request.OldName == request.NewName || len(request.NewName) > 64 || request.Ratio < 0 || math.IsNaN(request.Ratio) || math.IsInf(request.Ratio, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid group rename"})
		return
	}

	ratios := ratio_setting.GetGroupRatioCopy()
	if _, exists := ratios[request.OldName]; !exists {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "group does not exist"})
		return
	}
	if _, exists := ratios[request.NewName]; exists {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "group already exists"})
		return
	}
	delete(ratios, request.OldName)
	ratios[request.NewName] = request.Ratio

	renameList := func(values []string) []string {
		result := make([]string, 0, len(values))
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if value == request.OldName {
				value = request.NewName
			}
			if value != "" && !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
		return result
	}

	order := renameList(getGroupDisplayOrder())
	var groupGroupRatios map[string]map[string]float64
	if err := common.UnmarshalJsonStr(ratio_setting.GroupGroupRatio2JSONString(), &groupGroupRatios); err != nil {
		common.ApiError(c, err)
		return
	}
	if values, exists := groupGroupRatios[request.OldName]; exists {
		delete(groupGroupRatios, request.OldName)
		groupGroupRatios[request.NewName] = values
	}
	for _, values := range groupGroupRatios {
		if ratio, exists := values[request.OldName]; exists {
			delete(values, request.OldName)
			values[request.NewName] = ratio
		}
	}
	usableGroups := setting.GetUserUsableGroupsCopy()
	if description, exists := usableGroups[request.OldName]; exists {
		delete(usableGroups, request.OldName)
		usableGroups[request.NewName] = description
	}
	autoGroups := renameList(setting.GetAutoGroups())
	var topupRatios map[string]float64
	if err := common.UnmarshalJsonStr(common.TopupGroupRatio2JSONString(), &topupRatios); err != nil {
		common.ApiError(c, err)
		return
	}
	if ratio, exists := topupRatios[request.OldName]; exists {
		delete(topupRatios, request.OldName)
		topupRatios[request.NewName] = ratio
	}
	var rateLimits map[string][2]int
	if err := common.UnmarshalJsonStr(setting.ModelRequestRateLimitGroup2JSONString(), &rateLimits); err != nil {
		common.ApiError(c, err)
		return
	}
	if limits, exists := rateLimits[request.OldName]; exists {
		delete(rateLimits, request.OldName)
		rateLimits[request.NewName] = limits
	}

	values := map[string]any{
		"GroupRatio":                 ratios,
		groupDisplayOrderOption:      order,
		"GroupGroupRatio":            groupGroupRatios,
		"UserUsableGroups":           usableGroups,
		"AutoGroups":                 autoGroups,
		"TopupGroupRatio":            topupRatios,
		"ModelRequestRateLimitGroup": rateLimits,
	}
	options := make(map[string]string, len(values))
	for key, value := range values {
		raw, err := common.Marshal(value)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		options[key] = string(raw)
	}
	if err := model.RenameRoutingGroup(request.OldName, request.NewName, options); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "group.rename", map[string]any{"old_name": request.OldName, "new_name": request.NewName})
	common.ApiSuccess(c, gin.H{"ratios": ratios, "order": order})
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "自动",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
