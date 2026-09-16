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

	// 3. Test normal 200 pass-through without interference
	req200 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec200 := newCloseNotifyingRecorder()
	router.ServeHTTP(rec200, req200)

	assert.Equal(t, http.StatusOK, rec200.Code)
	assert.Contains(t, rec200.Body.String(), "gpt-4o")
}

func TestAuthenticateAndMigrateSubRouterUser(t *testing.T) {
	// Initialize test database
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}))

	defer func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.Unsetenv("SQL_DSN")
	}()

	// Mock upstream SubRouter server for login and token list
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dist/user/login":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "valid_old_user") && strings.Contains(string(body), "secret_pass_123") {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "upstream_sess_tok"})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"success": true,
					"message": "",
					"data": {
						"user": {
							"id": 88,
							"username": "valid_old_user",
							"display_name": "Old SubRouter User",
							"email": "olduser@subrouter.ai"
						}
					}
				}`))
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"success": false, "message": "用户名或密码错误"}`))
			}
		case "/api/dist/token/list":
			// Check cookie
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
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

	// 1. Failed credentials test
	_, err := AuthenticateAndMigrateSubRouterUser("wrong_user", "wrong_pass")
	require.ErrorIs(t, err, ErrSubRouterAuthFailed)

	// 2. Successful migration test
	user, err := AuthenticateAndMigrateSubRouterUser("valid_old_user", "secret_pass_123")
	require.NoError(t, err)
	require.NotNil(t, user)

	// Assert user created with STRICT zero quota (zero-capital principle)
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
}

func TestSyncCustomersFromRecords(t *testing.T) {
	// Initialize test database
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))

	defer func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.Unsetenv("SQL_DSN")
	}()

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
