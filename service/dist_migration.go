package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

var (
	ErrSubRouterAuthFailed           = errors.New("subrouter authentication failed")
	ErrSubRouterMigrationNotEligible = errors.New("subrouter password migration is not eligible")
	ErrSubRouterTokenSyncFailed      = errors.New("subrouter token sync failed")
)

type subRouterLoginResponse struct {
	Success bool                       `json:"success"`
	Message string                     `json:"message"`
	Data    subRouterLoginResponseData `json:"data"`
}

type subRouterLoginResponseData struct {
	Id          int                         `json:"id"`
	Username    string                      `json:"username"`
	DisplayName string                      `json:"display_name"`
	Email       string                      `json:"email"`
	Quota       *int64                      `json:"quota"`
	User        *subRouterLoginResponseData `json:"user"`
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

func unwrapSubRouterUser(data subRouterLoginResponseData) subRouterLoginResponseData {
	if data.User == nil {
		return data
	}
	user := *data.User
	if user.Quota == nil {
		user.Quota = data.Quota
	}
	return user
}

func authenticateSubRouterUser(username, password string, knownUserID int) (subRouterLoginResponseData, *http.Client, error) {
	baseURL := GetSubRouterBaseURL()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return subRouterLoginResponseData{}, nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second, Jar: jar}
	loginPayload, err := common.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return subRouterLoginResponseData{}, nil, err
	}

	var loginUser subRouterLoginResponseData
	loginSuccess := false
	for _, endpoint := range []string{baseURL + "/api/dist/user/login", baseURL + "/api/user/login"} {
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
		var parsed subRouterLoginResponse
		if common.Unmarshal(respBytes, &parsed) == nil && parsed.Success {
			loginUser = unwrapSubRouterUser(parsed.Data)
			loginSuccess = true
			break
		}
	}
	if !loginSuccess {
		return subRouterLoginResponseData{}, nil, ErrSubRouterAuthFailed
	}

	requestUserID := knownUserID
	if requestUserID <= 0 {
		requestUserID = loginUser.Id
	}
	for _, endpoint := range []string{baseURL + "/api/dist/user/self", baseURL + "/api/user/self"} {
		req, reqErr := http.NewRequest(http.MethodGet, endpoint, nil)
		if reqErr != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) SubRouter-Gateway/1.0")
		if requestUserID > 0 {
			req.Header.Set("New-Api-User", strconv.Itoa(requestUserID))
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			continue
		}
		respBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}
		var parsed subRouterLoginResponse
		if common.Unmarshal(respBytes, &parsed) != nil || !parsed.Success {
			continue
		}
		self := unwrapSubRouterUser(parsed.Data)
		if self.Id > 0 {
			loginUser.Id = self.Id
		}
		if self.Username != "" {
			loginUser.Username = self.Username
		}
		if self.DisplayName != "" {
			loginUser.DisplayName = self.DisplayName
		}
		if self.Email != "" {
			loginUser.Email = self.Email
		}
		if self.Quota != nil {
			loginUser.Quota = self.Quota
		}
		break
	}
	return loginUser, client, nil
}

// AuthenticateAndMigrateSubRouterUser verifies a legacy user with upstream
// SubRouter exactly once, then atomically creates or claims the local account
// with zero quota and imports its historical keys.
func AuthenticateAndMigrateSubRouterUser(username, password string) (*model.User, error) {
	var localUser model.User
	lookupErr := model.DB.Where("username = ? OR email = ?", username, model.NormalizeEmail(username)).First(&localUser).Error
	localUserExists := lookupErr == nil
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("load local migration user: %w", lookupErr)
	}

	var migrationMarker struct {
		SubRouterID int `json:"subrouter_id"`
	}
	if localUserExists && (localUser.Status != common.UserStatusEnabled || localUser.Password != "" || common.UnmarshalJsonStr(localUser.Setting, &migrationMarker) != nil || migrationMarker.SubRouterID <= 0) {
		return nil, ErrSubRouterMigrationNotEligible
	}
	if !localUserExists {
		localUser = model.User{}
	}

	knownLegacyUserID := 0
	if localUserExists {
		knownLegacyUserID = migrationMarker.SubRouterID
	}
	loginUser, client, err := authenticateSubRouterUser(username, password, knownLegacyUserID)
	if err != nil {
		return nil, err
	}
	if localUserExists {
		if loginUser.Id > 0 && loginUser.Id != migrationMarker.SubRouterID {
			return nil, ErrSubRouterMigrationNotEligible
		}
		if loginUser.Id <= 0 {
			loginUser.Id = migrationMarker.SubRouterID
		}
	} else if loginUser.Id <= 0 {
		return nil, ErrSubRouterMigrationNotEligible
	}
	if strings.TrimSpace(loginUser.Username) != "" {
		username = strings.TrimSpace(loginUser.Username)
	}
	displayName := strings.TrimSpace(loginUser.DisplayName)
	email := model.NormalizeEmail(loginUser.Email)
	var existingBalance struct {
		Quota *int64 `json:"subrouter_old_quota"`
	}
	if localUser.Setting != "" {
		_ = common.UnmarshalJsonStr(localUser.Setting, &existingBalance)
	}
	if loginUser.Quota == nil && existingBalance.Quota == nil {
		return nil, errors.New("SubRouter quota unavailable")
	}
	legacyQuota := localUser.LegacySubRouterQuota()
	legacyQuotaUpdatedAt := localUser.LegacySubRouterQuotaUpdatedAt()
	if loginUser.Quota != nil {
		if *loginUser.Quota < 0 || *loginUser.Quota > int64(common.MaxWalletQuota) {
			return nil, errors.New("invalid SubRouter quota")
		}
		legacyQuota = *loginUser.Quota
		legacyQuotaUpdatedAt = common.GetTimestamp()
	}
	mergedSetting, err := model.MergeLegacySubRouterSetting(localUser.Setting, loginUser.Id, legacyQuota, legacyQuotaUpdatedAt)
	if err != nil {
		return nil, err
	}

	// 2. Fetch historical API keys before completing password migration. If this
	// fails, the account remains eligible for a later retry instead of losing keys.
	baseURL := GetSubRouterBaseURL()
	tokenEndpoints := []string{
		baseURL + "/api/dist/token/list",
		baseURL + "/api/token/list",
	}

	var historicalTokens []subRouterTokenItem
	tokenListFetched := false
	for _, tokenEndpoint := range tokenEndpoints {
		tokenReq, err := http.NewRequest(http.MethodGet, tokenEndpoint, nil)
		if err != nil {
			continue
		}
		tokenReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) SubRouter-Gateway/1.0")
		tokenReq.Header.Set("New-Api-User", strconv.Itoa(loginUser.Id))

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
		if err := common.Unmarshal(tokenBytes, &parsedTokens); err == nil && parsedTokens.Success {
			historicalTokens = parsedTokens.Data
			tokenListFetched = true
			break
		}
	}
	if !tokenListFetched {
		return nil, ErrSubRouterTokenSyncFailed
	}

	// 3. Atomically claim the one-time migration and import the keys. The
	// conditional password update prevents concurrent requests from overwriting it.
	hashedPassword, err := common.Password2Hash(password)
	if err != nil {
		return nil, err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if localUserExists {
			updates := map[string]any{"password": hashedPassword, "setting": mergedSetting}
			if email != "" && localUser.Email == "" {
				updates["email"] = email
			}
			if displayName != "" && localUser.DisplayName == "" {
				updates["display_name"] = displayName
			}
			result := tx.Model(&model.User{}).Where("id = ? AND password = ?", localUser.Id, "").Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrSubRouterMigrationNotEligible
			}
			if _, err := model.IncrementUserAuthVersionWithTx(tx, localUser.Id); err != nil {
				return err
			}
		} else {
			var conflictCount int64
			conflicts := tx.Unscoped().Model(&model.User{}).Where("username = ?", username)
			if email != "" {
				conflicts = conflicts.Or("LOWER(email) = ?", email)
			}
			if err := conflicts.Count(&conflictCount).Error; err != nil {
				return err
			}
			if conflictCount > 0 {
				return ErrSubRouterMigrationNotEligible
			}
			localUser = model.User{
				Username:    username,
				Password:    hashedPassword,
				DisplayName: displayName,
				Email:       email,
				Role:        common.RoleCommonUser,
				Status:      common.UserStatusEnabled,
				Quota:       0,
				Group:       "default",
				AffCode:     common.GetRandomString(8),
				Setting:     mergedSetting,
				Remark:      fmt.Sprintf("SubRouter ID: %d", loginUser.Id),
			}
			if err := tx.Create(&localUser).Error; err != nil {
				return err
			}
		}

		for _, token := range historicalTokens {
			cleanKey := strings.TrimPrefix(token.Key, "sk-")
			if cleanKey == "" {
				continue
			}

			var count int64
			if err := tx.Model(&model.Token{}).Where(&model.Token{Key: cleanKey}).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}

			name := token.Name
			if name == "" {
				name = "Default API Key"
			}
			createdTime := token.CreatedTime
			if createdTime <= 0 {
				createdTime = common.GetTimestamp()
			}
			expiredTime := token.ExpiredTime
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
				Group:          model.LegacySubRouterGroup,
			}
			if err := tx.Create(&migratedToken).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if localUserExists {
		localUser.Password = hashedPassword
		localUser.Setting = mergedSetting
		if email != "" && localUser.Email == "" {
			localUser.Email = email
		}
		if displayName != "" && localUser.DisplayName == "" {
			localUser.DisplayName = displayName
		}
	}
	if err := model.PublishUserAuthCache(localUser.Id); err != nil {
		return nil, err
	}
	if _, err := model.RevokeAllUserSessions(localUser.Id, "legacy password migration"); err != nil {
		return nil, err
	}
	common.SysLog(fmt.Sprintf("[Migration] Completed one-time password and API key migration for user ID %d", localUser.Id))

	return &localUser, nil
}

// RefreshSubRouterLegacyBalance refreshes display-only legacy quota after a
// successful local password check. Failure never changes local authentication.
func RefreshSubRouterLegacyBalance(user *model.User, username, password string) error {
	if user == nil || user.Id <= 0 {
		return ErrSubRouterMigrationNotEligible
	}
	var marker struct {
		SubRouterID int `json:"subrouter_id"`
	}
	if common.UnmarshalJsonStr(user.Setting, &marker) != nil || marker.SubRouterID <= 0 {
		return nil
	}
	if user.LegacySubRouterQuotaUpdatedAt() > 0 {
		return nil
	}
	upstreamUser, _, err := authenticateSubRouterUser(username, password, marker.SubRouterID)
	if err != nil {
		return err
	}
	if upstreamUser.Id > 0 && upstreamUser.Id != marker.SubRouterID {
		return ErrSubRouterMigrationNotEligible
	}
	if upstreamUser.Quota == nil || *upstreamUser.Quota < 0 || *upstreamUser.Quota > int64(common.MaxWalletQuota) {
		return errors.New("SubRouter quota unavailable")
	}
	setting, err := model.MergeLegacySubRouterSetting(user.Setting, marker.SubRouterID, *upstreamUser.Quota, common.GetTimestamp())
	if err != nil {
		return err
	}
	if err := model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("setting", setting).Error; err != nil {
		return err
	}
	user.Setting = setting
	return nil
}

type SubRouterCustomerRecord struct {
	Id                      int     `json:"id"`
	Username                string  `json:"username"`
	DisplayName             string  `json:"display_name"`
	Email                   string  `json:"email"`
	Status                  int     `json:"status"`
	Quota                   int64   `json:"quota"`
	UsedQuota               int64   `json:"used_quota"`
	EffectiveCommissionRate float64 `json:"effective_commission_rate"`
	CommissionRateSource    string  `json:"commission_rate_source"`
}

type SubRouterCustomerListResponse struct {
	Success bool                      `json:"success"`
	Data    []SubRouterCustomerRecord `json:"data"`
	Total   int                       `json:"total"`
	Message string                    `json:"message"`
}

type SyncCustomersResult struct {
	TotalFetched  int `json:"total_fetched"`
	TotalInserted int `json:"total_inserted"`
	TotalUpdated  int `json:"total_updated"`
	TotalFailed   int `json:"total_failed"`
}

// SyncCustomersFromRecords writes or updates a list of SubRouter customer records into the local database
func SyncCustomersFromRecords(records []SubRouterCustomerRecord) *SyncCustomersResult {
	result := &SyncCustomersResult{
		TotalFetched: len(records),
	}

	for _, c := range records {
		username := strings.TrimSpace(c.Username)
		if username == "" {
			username = fmt.Sprintf("sub_%d", c.Id)
		}
		email := strings.TrimSpace(c.Email)

		// Look up if user already exists
		var existingUser model.User
		var found bool

		if c.Id > 0 {
			if err := model.DB.Where("remark LIKE ?", fmt.Sprintf("%%SubRouter ID: %d,%%", c.Id)).First(&existingUser).Error; err == nil {
				found = true
			}
		}
		if email != "" {
			if !found {
				if err := model.DB.Where("email = ?", email).First(&existingUser).Error; err == nil {
					found = true
				}
			}
		}
		if !found {
			if err := model.DB.Where("username = ?", username).First(&existingUser).Error; err == nil {
				// Only match if email also matches or either is empty
				if existingUser.Email == "" || email == "" || existingUser.Email == email {
					found = true
				}
			}
		}

		if found {
			needsSave := false
			if c.Id > 0 && !strings.Contains(existingUser.Remark, fmt.Sprintf("SubRouter ID: %d,", c.Id)) {
				existingUser.Remark = fmt.Sprintf("%s; SubRouter ID: %d, Old Quota: %d", strings.TrimSpace(existingUser.Remark), c.Id, c.Quota)
				needsSave = true
			}
			if email != "" && existingUser.Email == "" {
				existingUser.Email = email
				needsSave = true
			}
			if c.DisplayName != "" && existingUser.DisplayName != c.DisplayName {
				existingUser.DisplayName = c.DisplayName
				needsSave = true
			}
			if existingUser.AffCode == "" {
				for {
					aff := common.GetRandomString(8)
					var cnt int64
					model.DB.Model(&model.User{}).Where("aff_code = ?", aff).Count(&cnt)
					if cnt == 0 {
						existingUser.AffCode = aff
						needsSave = true
						break
					}
				}
			}
			if needsSave {
				_ = model.DB.Save(&existingUser).Error
			}
			result.TotalUpdated++
		} else {
			finalUsername := username
			for {
				var count int64
				model.DB.Model(&model.User{}).Where("username = ?", finalUsername).Count(&count)
				if count == 0 {
					break
				}
				finalUsername = fmt.Sprintf("%s_%d", username, c.Id)
			}

			// Generate guaranteed unique AffCode to prevent UNIQUE constraint collisions
			var affCode string
			for {
				candidate := common.GetRandomString(8)
				var cnt int64
				model.DB.Model(&model.User{}).Where("aff_code = ?", candidate).Count(&cnt)
				if cnt == 0 {
					affCode = candidate
					break
				}
			}

			newUser := model.User{
				Username:    finalUsername,
				Password:    "",
				DisplayName: c.DisplayName,
				Email:       email,
				Role:        common.RoleCommonUser,
				Status:      c.Status,
				Quota:       0, // Strict zero capital principle
				Group:       "default",
				AffCode:     affCode,
				Remark:      fmt.Sprintf("SubRouter ID: %d, Old Quota: %d", c.Id, c.Quota),
			}
			if newUser.Status == 0 {
				newUser.Status = common.UserStatusEnabled
			}
			if strings.HasPrefix(c.Username, "github_") {
				newUser.GitHubId = strings.TrimPrefix(c.Username, "github_")
			}
			if strings.HasPrefix(c.Username, "google_") {
				newUser.OidcId = strings.TrimPrefix(c.Username, "google_")
			}

			meta := map[string]any{
				"subrouter_id":         c.Id,
				"subrouter_old_quota":  c.Quota,
				"subrouter_used_quota": c.UsedQuota,
			}
			if metaBytes, mErr := common.Marshal(meta); mErr == nil {
				newUser.Setting = string(metaBytes)
			}

			if err := model.DB.Create(&newUser).Error; err != nil {
				common.SysError(fmt.Sprintf("Failed to insert customer %s (ID %d): %v", finalUsername, c.Id, err))
				result.TotalFailed++
			} else {
				result.TotalInserted++
			}
		}
	}

	return result
}

// SyncSubRouterCustomers pulls all distributor customers and writes them into local DB
func SyncSubRouterCustomers(cookie string, distributorUID int) (*SyncCustomersResult, []SubRouterCustomerRecord, error) {
	baseURL := strings.TrimSpace(os.Getenv("SUBROUTER_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://subrouter.ai"
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}

	var allRecords []SubRouterCustomerRecord
	page := 1
	pageSize := 100

	for {
		reqURL := fmt.Sprintf("%s/api/distributor/customers?page=%d&page_size=%d&sort=id&order=desc",
			strings.TrimRight(baseURL, "/"), page, pageSize)

		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, allRecords, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("New-Api-User", strconv.Itoa(distributorUID))
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) SubRouter-Gateway/1.0")

		resp, err := client.Do(req)
		if err != nil {
			return nil, allRecords, fmt.Errorf("request failed on page %d: %w", page, err)
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, allRecords, fmt.Errorf("failed to read response body: %w", err)
		}

		var parsed SubRouterCustomerListResponse
		if err := common.Unmarshal(bodyBytes, &parsed); err != nil {
			return nil, allRecords, fmt.Errorf("failed to unmarshal JSON: %w", err)
		}
		if !parsed.Success {
			return nil, allRecords, fmt.Errorf("upstream returned failure on page %d: %s", page, parsed.Message)
		}

		if len(parsed.Data) == 0 {
			break
		}

		allRecords = append(allRecords, parsed.Data...)

		if len(allRecords) >= parsed.Total || len(parsed.Data) < pageSize {
			break
		}
		page++
	}

	result := SyncCustomersFromRecords(allRecords)
	return result, allRecords, nil
}
