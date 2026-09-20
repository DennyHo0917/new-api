package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type distCloseNotifyingRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (c *distCloseNotifyingRecorder) CloseNotify() <-chan bool {
	return c.closed
}

func newDistRecorder() *distCloseNotifyingRecorder {
	return &distCloseNotifyingRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closed:           make(chan bool, 1),
	}
}

func TestDistDualTrackGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Mock upstream SubRouter server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Test-Upstream") == "rate-limit" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"too many requests","code":"rate_limit_exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"origin":"subrouter_upstream"}`))
	}))
	defer upstream.Close()

	require.NoError(t, os.Setenv("SUBROUTER_BASE_URL", upstream.URL))
	defer func() {
		_ = os.Unsetenv("SUBROUTER_BASE_URL")
	}()

	// Init in-memory test DB
	originalRedis := common.RedisEnabled
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}))

	defer func() {
		common.RedisEnabled = originalRedis
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.Unsetenv("SQL_DSN")
	}()

	// 1. Create a local active user with positive quota
	localActiveUser := model.User{
		Username: "local_user_with_quota",
		Password: "password123",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Quota:    5000000,
	}
	require.NoError(t, localActiveUser.Insert(0))
	localActiveToken := model.Token{
		UserId:         localActiveUser.Id,
		Name:           "Local Active Key",
		Key:            "localactivekey12345678901234567890123456789012",
		Status:         common.TokenStatusEnabled,
		UnlimitedQuota: true,
		ExpiredTime:    -1,
	}
	require.NoError(t, model.DB.Create(&localActiveToken).Error)

	// 2. Create a migrated SubRouter user with ZERO quota
	migratedUser := model.User{
		Username: "migrated_user_zero_quota",
		Password: "password123",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Quota:    0,
	}
	require.NoError(t, migratedUser.Insert(0))
	_ = model.DB.Model(&migratedUser).Update("quota", 0).Error

	migratedToken := model.Token{
		UserId:         migratedUser.Id,
		Name:           "Migrated Old Key",
		Key:            "subroutermigratedkey123456789012345678901234",
		Status:         common.TokenStatusEnabled,
		UnlimitedQuota: true,
		ExpiredTime:    -1,
		Group:          "subrouter", // Tagged as legacy SubRouter key
	}
	require.NoError(t, model.DB.Create(&migratedToken).Error)

	// Setup Gin router with DistDualTrackGateway
	router := gin.New()
	router.Use(DistDualTrackGateway())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		// If request reached here, it passed to local processing!
		c.JSON(http.StatusOK, gin.H{"origin": "local_new_api"})
	})

	// Case A: Unmigrated key (not in local DB) -> Must be proxied to SubRouter upstream
	reqA := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqA.Header.Set("Authorization", "Bearer sk-completelyunknownkey12345")
	recA := newDistRecorder()
	router.ServeHTTP(recA, reqA)
	assert.Equal(t, http.StatusOK, recA.Code)
	assert.Contains(t, recA.Body.String(), "subrouter_upstream", "Unmigrated key must proxy to SubRouter")

	// Case B: Local key with positive quota -> Must route to local New API
	reqB := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqB.Header.Set("Authorization", "Bearer sk-localactivekey12345678901234567890123456789012")
	recB := newDistRecorder()
	router.ServeHTTP(recB, reqB)
	assert.Equal(t, http.StatusOK, recB.Code)
	assert.Contains(t, recB.Body.String(), "local_new_api", "Active key with quota must route to local")

	// Case C: Migrated SubRouter key with ZERO local quota -> Must proxy to SubRouter to consume old balance
	reqC := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqC.Header.Set("Authorization", "Bearer sk-subroutermigratedkey123456789012345678901234")
	recC := newDistRecorder()
	router.ServeHTTP(recC, reqC)
	assert.Equal(t, http.StatusOK, recC.Code)
	assert.Contains(t, recC.Body.String(), "subrouter_upstream", "Migrated key with 0 quota must proxy to SubRouter")

	// Case D: Temporary upstream rate limiting must not mark the old balance exhausted.
	reqD := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqD.Header.Set("Authorization", "Bearer sk-subroutermigratedkey123456789012345678901234")
	reqD.Header.Set("X-Test-Upstream", "rate-limit")
	recD := newDistRecorder()
	router.ServeHTTP(recD, reqD)
	assert.Equal(t, http.StatusTooManyRequests, recD.Code)
	assert.Contains(t, recD.Body.String(), "rate_limit_exceeded")
	require.NoError(t, model.DB.First(&migratedToken, migratedToken.Id).Error)
	assert.Equal(t, model.LegacySubRouterGroup, migratedToken.Group)
}
