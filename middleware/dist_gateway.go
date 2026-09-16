package middleware

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// extractClientApiKey extracts the raw API key from various request locations
func extractClientApiKey(c *gin.Context) string {
	key := c.Request.Header.Get("Authorization")
	if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	if key == "" || key == "midjourney-proxy" {
		if mj := c.Request.Header.Get("mj-api-secret"); mj != "" {
			key = strings.TrimPrefix(mj, "Bearer ")
			key = strings.TrimPrefix(key, "bearer ")
		}
	}
	if key == "" {
		key = c.Request.Header.Get("x-api-key")
	}
	if key == "" {
		key = c.Request.Header.Get("x-goog-api-key")
	}
	if key == "" {
		key = c.Query("key")
	}
	return strings.TrimSpace(key)
}

// DistDualTrackGateway provides intelligent dual-track dispatching for relay requests.
// - If key does not exist locally: transparently proxies to SubRouter (preserves SSE streaming).
// - If key exists locally and user has positive quota: routes to local New API channels.
// - If key exists locally with 0 quota and is a migrated SubRouter key: proxies to SubRouter to exhaust old balance.
// - When SubRouter returns 429 Insufficient Quota: returns friendly prompt to top up on www.api-route.com.
func DistDualTrackGateway() gin.HandlerFunc {
	return func(c *gin.Context) {
		rawKey := extractClientApiKey(c)
		if rawKey == "" {
			// No key provided, let downstream TokenAuth handle standard 401
			c.Next()
			return
		}

		// Clean key formatting following standard New API conventions
		cleanKey := strings.TrimPrefix(rawKey, "sk-")
		parts := strings.Split(cleanKey, "-")
		lookupKey := parts[0]

		// 1. Check local database for key existence
		token, err := model.GetTokenByKey(lookupKey, false)
		if err != nil && lookupKey != rawKey {
			token, err = model.GetTokenByKey(rawKey, false)
		}

		// Key NOT found in local database -> Legacy SubRouter customer key
		if token == nil || err != nil {
			service.ProxyToSubRouter(c)
			return
		}

		// 2. Key exists locally -> check account status and quota
		user, err := model.GetUserById(token.UserId, false)
		if err != nil || user == nil {
			service.ProxyToSubRouter(c)
			return
		}

		userEnabled := user.Status == common.UserStatusEnabled
		tokenEnabled := token.Status == common.TokenStatusEnabled
		tokenNotExpired := token.ExpiredTime == -1 || token.ExpiredTime > common.GetTimestamp()
		if !userEnabled || !tokenEnabled || !tokenNotExpired {
			c.Next()
			return
		}

		// Legacy keys stay on SubRouter until that upstream explicitly reports
		// quota exhaustion. This prevents local funds from paying old balances.
		if model.IsLegacySubRouterToken(token) && !model.IsLegacySubRouterExhausted(token) {
			if service.ProxyToSubRouter(c) {
				if err := model.MarkLegacySubRouterTokenExhausted(token.Id); err != nil {
					common.SysError("failed to mark legacy token exhausted: " + err.Error())
				}
			}
			return
		}

		tokenHasQuota := token.UnlimitedQuota || token.RemainQuota > 0
		userHasQuota := user.Quota > 0

		// Local account is active and has positive quota -> route locally
		if userEnabled && tokenEnabled && tokenNotExpired && tokenHasQuota && userHasQuota {
			c.Next()
			return
		}

		// Otherwise proceed to normal local handling (e.g. returns standard local quota exhausted)
		c.Next()
	}
}
