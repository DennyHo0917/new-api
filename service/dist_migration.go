package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var (
	ErrSubRouterAuthFailed = errors.New("subrouter authentication failed")
)

type subRouterLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type subRouterTokenItem struct {
	Id             int    `json:"id"`
	Name           string `json:"name"`
	Key            string `json:"key"`
	Status         int    `json:"status"`
	RemainQuota    int    `json:"remain_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	CreatedTime    int64  `json:"created_time"`
	ExpiredTime    int64  `json:"expired_time"`
}

type subRouterTokenListResponse struct {
	Success bool                 `json:"success"`
	Message string               `json:"message"`
	Data    []subRouterTokenItem `json:"data"`
}

// AuthenticateAndMigrateSubRouterUser verifies user credentials with upstream SubRouter.
// If valid, creates/updates the local user with zero initial quota (zero-capital principle)
// and pulls historical API keys into the local database tagged with Group="subrouter".
func AuthenticateAndMigrateSubRouterUser(username, password string) (*model.User, error) {
	baseURL := GetSubRouterBaseURL()

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}

	loginPayload, err := common.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return nil, err
	}

	// 1. Try /api/dist/user/login first, then fallback to /api/user/login
	loginEndpoints := []string{
		baseURL + "/api/dist/user/login",
		baseURL + "/api/user/login",
	}

	var loginRespBody []byte
	var loginSuccess bool

	for _, endpoint := range loginEndpoints {
		req, reqErr := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(loginPayload))
		if reqErr != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) SubRouter-Gateway/1.0")

		resp, respErr := client.Do(req)
		if respErr != nil {
			common.SysLog(fmt.Sprintf("SubRouter login request error to %s: %v", endpoint, respErr))
			continue
		}
		respBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}

		var parsedResp subRouterLoginResponse
		if unmarshalErr := common.Unmarshal(respBytes, &parsedResp); unmarshalErr == nil && parsedResp.Success {
			loginRespBody = respBytes
			loginSuccess = true
			break
		}
	}

	if !loginSuccess {
		return nil, ErrSubRouterAuthFailed
	}

	// Parse user details from SubRouter response
	displayName := username
	email := ""
	var parsedResp subRouterLoginResponse
	_ = common.Unmarshal(loginRespBody, &parsedResp)

	if dataMap, ok := parsedResp.Data.(map[string]any); ok {
		if userMap, ok := dataMap["user"].(map[string]any); ok {
			if u, ok := userMap["username"].(string); ok && u != "" {
				username = u
			}
			if dn, ok := userMap["display_name"].(string); ok && dn != "" {
				displayName = dn
			}
			if em, ok := userMap["email"].(string); ok && em != "" {
				email = em
			}
		} else {
			if u, ok := dataMap["username"].(string); ok && u != "" {
				username = u
			}
			if dn, ok := dataMap["display_name"].(string); ok && dn != "" {
				displayName = dn
			}
			if em, ok := dataMap["email"].(string); ok && em != "" {
				email = em
			}
		}
	}

	// 2. Synchronize local User
	var localUser model.User
	query := model.DB.Where("username = ?", username)
	if email != "" {
		query = model.DB.Where("username = ? OR email = ?", username, email)
	}

	userExists := query.First(&localUser).Error == nil

	if !userExists {
		localUser = model.User{
			Username:    username,
			Password:    password,
			DisplayName: displayName,
			Email:       email,
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
		}
		if insertErr := localUser.Insert(0); insertErr != nil {
			return nil, fmt.Errorf("failed to create local migrated user: %w", insertErr)
		}
		// Strict zero-capital principle: zero out quota regardless of new-user bonus
		if localUser.Quota != 0 {
			localUser.Quota = 0
			_ = model.DB.Model(&localUser).Update("quota", 0).Error
		}
		common.SysLog(fmt.Sprintf("[Migration] Created new local user %s (ID: %d) from SubRouter with quota 0", username, localUser.Id))
	} else {
		hashedPwd, pwdErr := common.Password2Hash(password)
		if pwdErr == nil {
			localUser.Password = hashedPwd
		}
		if email != "" && localUser.Email == "" {
			localUser.Email = email
		}
		if displayName != "" && localUser.DisplayName == "" {
			localUser.DisplayName = displayName
		}
		_ = model.DB.Save(&localUser).Error
		common.SysLog(fmt.Sprintf("[Migration] Updated password and credentials for existing user %s (ID: %d)", username, localUser.Id))
	}

	// 3. Fetch historical API keys from SubRouter using authenticated session
	tokenEndpoints := []string{
		baseURL + "/api/dist/token/list",
		baseURL + "/api/token/list",
	}

	for _, tokenEndpoint := range tokenEndpoints {
		tokenReq, err := http.NewRequest(http.MethodGet, tokenEndpoint, nil)
		if err != nil {
			continue
		}
		tokenReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) SubRouter-Gateway/1.0")

		tokenResp, err := client.Do(tokenReq)
		if err != nil {
			continue
		}
		tokenBytes, err := io.ReadAll(tokenResp.Body)
		_ = tokenResp.Body.Close()
		if err != nil {
			continue
		}

		var parsedTokens subRouterTokenListResponse
		if err := common.Unmarshal(tokenBytes, &parsedTokens); err == nil && parsedTokens.Success && len(parsedTokens.Data) > 0 {
			for _, t := range parsedTokens.Data {
				cleanKey := strings.TrimPrefix(t.Key, "sk-")
				if cleanKey == "" {
					continue
				}

				// Check if token already exists locally
				existingToken, _ := model.GetTokenByKey(cleanKey, true)
				if existingToken == nil {
					name := t.Name
					if name == "" {
						name = "Default API Key"
					}
					createdTime := t.CreatedTime
					if createdTime <= 0 {
						createdTime = common.GetTimestamp()
					}
					expiredTime := t.ExpiredTime
					if expiredTime == 0 {
						expiredTime = -1
					}

					migratedToken := model.Token{
						UserId:         localUser.Id,
						Name:           name,
						Key:            cleanKey,
						Status:         common.TokenStatusEnabled,
						RemainQuota:    0,
						UnlimitedQuota: true,
						CreatedTime:    createdTime,
						AccessedTime:   common.GetTimestamp(),
						ExpiredTime:    expiredTime,
						Group:          "subrouter", // Tagged as legacy SubRouter key
					}
					if err := model.DB.Create(&migratedToken).Error; err == nil {
						common.SysLog(fmt.Sprintf("[Migration] Imported token %s (%s) for user %d", name, cleanKey[:min(8, len(cleanKey))]+"...", localUser.Id))
					}
				}
			}
			break // Successfully imported from first working token endpoint
		}
	}

	return &localUser, nil
}
