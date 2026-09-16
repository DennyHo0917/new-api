package service

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
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

		if email != "" {
			if err := model.DB.Where("email = ?", email).First(&existingUser).Error; err == nil {
				found = true
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

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	if proxyEnv := os.Getenv("HTTP_PROXY"); proxyEnv == "" {
		if proxyEnv = os.Getenv("http_proxy"); proxyEnv == "" {
			if pURL, err := url.Parse("http://127.0.0.1:7897"); err == nil {
				transport.Proxy = http.ProxyURL(pURL)
			}
		}
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
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

