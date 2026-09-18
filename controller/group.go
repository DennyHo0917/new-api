package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

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
