package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetSubRouterProxy() {
	subRouterProxyOnce = sync.Once{}
	subRouterProxy = nil
}

func setupDistProxyTestDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousSQLitePath := common.SQLitePath
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	if postgresDSN := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")); postgresDSN != "" {
		common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
		require.NoError(t, os.Setenv("SQL_DSN", postgresDSN))
	} else {
		common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
		common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
		require.NoError(t, os.Setenv("SQL_DSN", "local"))
	}
	require.NoError(t, model.InitDB())
	testDB := model.DB

	t.Cleanup(func() {
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SQLitePath = previousSQLitePath
		common.SetDatabaseTypes(previousMainType, previousLogType)
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", previousSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
	})
}

type closeNotifyingRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func newCloseNotifyingRecorder() *closeNotifyingRecorder {
	return &closeNotifyingRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closed:           make(chan bool, 1),
	}
}

func (c *closeNotifyingRecorder) CloseNotify() <-chan bool {
	return c.closed
}

func TestSubRouterReverseProxy_QuotaInterception(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Mock upstream SubRouter server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			// Simulate 429 Insufficient Quota from SubRouter
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"insufficient user quota","type":"insufficient_quota","code":"insufficient_user_quota"}}`))
		case "/v1/embeddings":
			// Simulate 403 with quota message
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"user quota not enough to complete request"}}`))
		case "/v1/rate-limited":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"too many requests","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
		case "/v1/payment-required":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"payment method required","code":"payment_required"}}`))
		case "/v1/models":
			// Normal 200 OK
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	// Configure environment to point proxy to mock upstream
	require.NoError(t, os.Setenv("SUBROUTER_BASE_URL", upstream.URL))
	defer func() {
		_ = os.Unsetenv("SUBROUTER_BASE_URL")
		resetSubRouterProxy()
	}()
	resetSubRouterProxy()

	// 1. Test 429 Insufficient Quota Interception
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		ProxyToSubRouter(c)
	})
	router.POST("/v1/embeddings", func(c *gin.Context) {
		ProxyToSubRouter(c)
	})
	var rateLimitedExhausted bool
	router.POST("/v1/rate-limited", func(c *gin.Context) {
		rateLimitedExhausted = ProxyToSubRouter(c)
	})
	var paymentRequiredExhausted bool
	router.POST("/v1/payment-required", func(c *gin.Context) {
		paymentRequiredExhausted = ProxyToSubRouter(c)
	})
	router.GET("/v1/models", func(c *gin.Context) {
		ProxyToSubRouter(c)
	})

	req429 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec429 := newCloseNotifyingRecorder()
	router.ServeHTTP(rec429, req429)

	assert.Equal(t, http.StatusTooManyRequests, rec429.Code)
	assert.Contains(t, rec429.Body.String(), QuotaExhaustedMessage)
	assert.Contains(t, rec429.Body.String(), "insufficient_user_quota")

	// 2. Test 403 with quota string converted to 429 with prompt
	req403 := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{}`))
	rec403 := newCloseNotifyingRecorder()
	router.ServeHTTP(rec403, req403)

	assert.Equal(t, http.StatusTooManyRequests, rec403.Code)
	assert.Contains(t, rec403.Body.String(), QuotaExhaustedMessage)

	// 3. Generic rate limiting and payment errors must pass through without
	// marking the legacy key exhausted.
	reqRateLimited := httptest.NewRequest(http.MethodPost, "/v1/rate-limited", strings.NewReader(`{}`))
	recRateLimited := newCloseNotifyingRecorder()
	router.ServeHTTP(recRateLimited, reqRateLimited)
	assert.Equal(t, http.StatusTooManyRequests, recRateLimited.Code)
	assert.Contains(t, recRateLimited.Body.String(), "rate_limit_exceeded")
	assert.NotContains(t, recRateLimited.Body.String(), QuotaExhaustedMessage)
	assert.False(t, rateLimitedExhausted)

	reqPaymentRequired := httptest.NewRequest(http.MethodPost, "/v1/payment-required", strings.NewReader(`{}`))
	recPaymentRequired := newCloseNotifyingRecorder()
	router.ServeHTTP(recPaymentRequired, reqPaymentRequired)
	assert.Equal(t, http.StatusPaymentRequired, recPaymentRequired.Code)
	assert.Contains(t, recPaymentRequired.Body.String(), "payment_required")
	assert.NotContains(t, recPaymentRequired.Body.String(), QuotaExhaustedMessage)
	assert.False(t, paymentRequiredExhausted)

	// 4. Test normal 200 pass-through without interference
	req200 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec200 := newCloseNotifyingRecorder()
	router.ServeHTTP(rec200, req200)

	assert.Equal(t, http.StatusOK, rec200.Code)
	assert.Contains(t, rec200.Body.String(), "gpt-4o")
}

func TestAuthenticateAndMigrateSubRouterUser(t *testing.T) {
	setupDistProxyTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}))

	legacySetting := `{"subrouter_id":88}`
	legacyUser := model.User{Username: "valid_old_user", Password: "", DisplayName: "Old SubRouter User", Email: "olduser@subrouter.ai", Status: common.UserStatusEnabled, Quota: 0, Setting: legacySetting, AffCode: "legacy01"}
	require.NoError(t, model.DB.Create(&legacyUser).Error)
	wrongPasswordUser := model.User{Username: "wrong_user", Password: "", Status: common.UserStatusEnabled, Setting: `{"subrouter_id":89}`, AffCode: "legacy02"}
	require.NoError(t, model.DB.Create(&wrongPasswordUser).Error)
	tokenFailureUser := model.User{Username: "token_failure_user", Password: "", Status: common.UserStatusEnabled, Setting: `{"subrouter_id":90}`, AffCode: "legacy03"}
	require.NoError(t, model.DB.Create(&tokenFailureUser).Error)
	unmarkedUser := model.User{Username: "unmarked_user", Password: "", Status: common.UserStatusEnabled, Setting: `{}`, AffCode: "legacy04"}
	require.NoError(t, model.DB.Create(&unmarkedUser).Error)
	migratedHash, err := common.HashAccountPassword("existing_password_123")
	require.NoError(t, err)
	alreadyMigratedUser := model.User{Username: "already_migrated", Password: migratedHash, Status: common.UserStatusEnabled, Setting: `{"subrouter_id":91}`, AffCode: "legacy05"}
	require.NoError(t, model.DB.Create(&alreadyMigratedUser).Error)

	loginRequests := 0
	selfRequests := 0
	tokenRequests := 0
	missingTokenIdentity := false

	// Mock upstream SubRouter server for login and token list.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dist/user/login":
			loginRequests++
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "valid_old_user") && strings.Contains(string(body), "secret_pass_123") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "upstream_sess_tok", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"success": true,
					"message": "",
					"data": {
						"user": {
							"username": "valid_old_user",
							"display_name": "Old SubRouter User",
							"email": "olduser@subrouter.ai"
						}
					}
				}`))
			} else if strings.Contains(string(body), "new_old_user") && strings.Contains(string(body), "secret_pass_123") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "new_upstream_sess", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
			} else if strings.Contains(string(body), "new_token_failure_user") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "new_token_failure", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"user":{"id":93,"username":"new_token_failure_user","email":"new-token-failure@example.com"}}}`))
			} else if strings.Contains(string(body), "token_failure_user") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "token_failure", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"user":{"id":90,"username":"token_failure_user"}}}`))
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"success": false, "message": "用户名或密码错误"}`))
			}
		case "/api/dist/user/self", "/api/user/self":
			selfRequests++
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "new_upstream_sess" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":92,"username":"new_old_user","display_name":"New Old User","email":"new-old@example.com"}}`))
		case "/api/dist/token/list", "/api/token/list":
			tokenRequests++
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value == "token_failure" || cookie.Value == "new_token_failure" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false}`))
				return
			}
			if cookie.Value != "upstream_sess_tok" && cookie.Value != "new_upstream_sess" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			expectedUserID := "88"
			if cookie.Value == "new_upstream_sess" {
				expectedUserID = "92"
			}
			if r.Header.Get("New-Api-User") != expectedUserID {
				missingTokenIdentity = true
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if cookie.Value == "new_upstream_sess" {
				_, _ = w.Write([]byte(`{"success":true,"data":[{"id":102,"name":"New Historical Key","key":"sk-newsuboldkey1234567890abcdef","status":1,"unlimited_quota":true,"created_time":1700000001}]}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"success": true,
				"data": [
					{
						"id": 101,
						"name": "Historical Project Key",
						"key": "sk-suboldkey1234567890abcdef",
						"status": 1,
						"remain_quota": 500000,
						"unlimited_quota": true,
						"created_time": 1700000000
					}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	require.NoError(t, os.Setenv("SUBROUTER_BASE_URL", upstream.URL))
	defer func() {
		_ = os.Unsetenv("SUBROUTER_BASE_URL")
	}()

	// Existing unmarked or already migrated accounts must never reach upstream.
	_, err = AuthenticateAndMigrateSubRouterUser("unmarked_user", "irrelevant_password")
	require.ErrorIs(t, err, ErrSubRouterMigrationNotEligible)
	_, err = AuthenticateAndMigrateSubRouterUser("already_migrated", "wrong_password")
	require.ErrorIs(t, err, ErrSubRouterMigrationNotEligible)
	assert.Equal(t, 0, loginRequests)

	// Unknown and eligible placeholder accounts may reach upstream, but bad
	// credentials do not create or modify a local account.
	_, err = AuthenticateAndMigrateSubRouterUser("absent_user", "irrelevant_password")
	require.ErrorIs(t, err, ErrSubRouterAuthFailed)
	var absentCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "absent_user").Count(&absentCount).Error)
	assert.Zero(t, absentCount)
	assert.Equal(t, 1, loginRequests)

	_, err = AuthenticateAndMigrateSubRouterUser("wrong_user", "wrong_pass")
	require.ErrorIs(t, err, ErrSubRouterAuthFailed)
	assert.Equal(t, 2, loginRequests)

	// Successful upstream login carries its session cookie to the key-list request.
	user, err := AuthenticateAndMigrateSubRouterUser("valid_old_user", "secret_pass_123")
	require.NoError(t, err)
	require.NotNil(t, user)

	// The pre-synchronized user keeps strict zero quota and receives a local hash.
	assert.Equal(t, "valid_old_user", user.Username)
	assert.Equal(t, "olduser@subrouter.ai", user.Email)
	assert.Equal(t, 0, user.Quota, "Migrated user MUST have 0 quota (operator does not front capital)")
	assert.True(t, common.ValidatePasswordAndHash("secret_pass_123", user.Password))

	// Assert token imported
	token, err := model.GetTokenByKey("suboldkey1234567890abcdef", true)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "Historical Project Key", token.Name)
	assert.Equal(t, user.Id, token.UserId)
	assert.Equal(t, "subrouter", token.Group, "Migrated token must have group tagged as subrouter")
	assert.Equal(t, 3, loginRequests)
	assert.Equal(t, 1, tokenRequests)
	assert.False(t, missingTokenIdentity)

	// Once the password is present, another migration attempt cannot call or overwrite upstream.
	_, err = AuthenticateAndMigrateSubRouterUser("valid_old_user", "replacement_password_123")
	require.ErrorIs(t, err, ErrSubRouterMigrationNotEligible)
	assert.Equal(t, 3, loginRequests)
	var persisted model.User
	require.NoError(t, model.DB.First(&persisted, legacyUser.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("secret_pass_123", persisted.Password))
	assert.False(t, common.ValidatePasswordAndHash("replacement_password_123", persisted.Password))

	// A key-list failure leaves the password blank so a safe retry remains possible.
	_, err = AuthenticateAndMigrateSubRouterUser("token_failure_user", "secret_pass_123")
	require.ErrorIs(t, err, ErrSubRouterTokenSyncFailed)
	persisted = model.User{}
	require.NoError(t, model.DB.First(&persisted, tokenFailureUser.Id).Error)
	assert.Empty(t, persisted.Password)

	// A successful upstream login for an unknown legacy user creates one local
	// zero-quota account and imports its keys in the same transaction.
	newUser, err := AuthenticateAndMigrateSubRouterUser("new_old_user", "secret_pass_123")
	require.NoError(t, err)
	assert.Equal(t, "new_old_user", newUser.Username)
	assert.Equal(t, "new-old@example.com", newUser.Email)
	assert.Zero(t, newUser.Quota)
	assert.True(t, common.ValidatePasswordAndHash("secret_pass_123", newUser.Password))
	assert.Contains(t, newUser.Setting, `"subrouter_id":92`)
	newToken, err := model.GetTokenByKey("newsuboldkey1234567890abcdef", true)
	require.NoError(t, err)
	assert.Equal(t, newUser.Id, newToken.UserId)
	assert.Equal(t, model.LegacySubRouterGroup, newToken.Group)
	assert.Equal(t, 3, selfRequests)
	assert.False(t, missingTokenIdentity)

	// Token synchronization failure rolls back on-demand account creation.
	_, err = AuthenticateAndMigrateSubRouterUser("new_token_failure_user", "secret_pass_123")
	require.ErrorIs(t, err, ErrSubRouterTokenSyncFailed)
	var failedCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "new_token_failure_user").Count(&failedCount).Error)
	assert.Zero(t, failedCount)
}

func TestSyncCustomersFromRecords(t *testing.T) {
	setupDistProxyTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))

	records := []SubRouterCustomerRecord{
		{
			Id:          1001,
			Username:    "alice",
			DisplayName: "Alice Smith",
			Email:       "alice@example.com",
			Status:      1,
			Quota:       50000,
			UsedQuota:   12000,
		},
		{
			Id:          1002,
			Username:    "alice", // Collision on username
			DisplayName: "Alice Two",
			Email:       "alice2@example.com",
			Status:      1,
			Quota:       30000,
			UsedQuota:   5000,
		},
		{
			Id:          1003,
			Username:    "bob",
			DisplayName: "Bob Jones",
			Email:       "bob@example.com",
			Status:      1,
			Quota:       0,
			UsedQuota:   0,
		},
	}

	result := SyncCustomersFromRecords(records)
	assert.Equal(t, 3, result.TotalFetched)
	assert.Equal(t, 3, result.TotalInserted)
	assert.Equal(t, 0, result.TotalFailed)

	// Verify Alice 1
	var u1 model.User
	require.NoError(t, model.DB.Where("email = ?", "alice@example.com").First(&u1).Error)
	assert.Equal(t, "alice", u1.Username)
	assert.Equal(t, 0, u1.Quota, "Strict zero-capital invariant")
	assert.NotEmpty(t, u1.AffCode)
	assert.Contains(t, u1.Remark, "SubRouter ID: 1001")

	// Verify Alice 2 (deduplicated username)
	var u2 model.User
	require.NoError(t, model.DB.Where("email = ?", "alice2@example.com").First(&u2).Error)
	assert.Equal(t, "alice_1002", u2.Username)
	assert.NotEqual(t, u1.AffCode, u2.AffCode, "AffCodes must be unique")

	// Verify update path (idempotency)
	recordsUpdate := []SubRouterCustomerRecord{
		{
			Id:          1001,
			Username:    "alice",
			DisplayName: "Alice Updated",
			Email:       "alice@example.com",
			Status:      1,
		},
	}
	resultUpdate := SyncCustomersFromRecords(recordsUpdate)
	assert.Equal(t, 1, resultUpdate.TotalUpdated)
	assert.Equal(t, 0, resultUpdate.TotalInserted)
	assert.Equal(t, 0, resultUpdate.TotalFailed)

	var u1Updated model.User
	require.NoError(t, model.DB.Where("email = ?", "alice@example.com").First(&u1Updated).Error)
	assert.Equal(t, "Alice Updated", u1Updated.DisplayName)
}
