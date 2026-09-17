package model

import (
	"encoding/base64"
	"maps"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
)

func TestEnvironmentOptionOverridesWinWithoutPersisting(t *testing.T) {
	originalMap := maps.Clone(common.OptionMap)
	originalServerAddress := system_setting.ServerAddress
	originalPayAddress, originalEpayID, originalEpayKey := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey
	originalStripeSecret, originalWebhookSecret, originalPriceID := setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId
	originalGitHubID, originalGitHubSecret, originalGitHubEnabled := common.GitHubClientId, common.GitHubClientSecret, common.GitHubOAuthEnabled
	t.Cleanup(func() {
		common.OptionMap = originalMap
		system_setting.ServerAddress = originalServerAddress
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = originalPayAddress, originalEpayID, originalEpayKey
		setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = originalStripeSecret, originalWebhookSecret, originalPriceID
		common.GitHubClientId, common.GitHubClientSecret, common.GitHubOAuthEnabled = originalGitHubID, originalGitHubSecret, originalGitHubEnabled
	})
	common.OptionMap = map[string]string{}
	t.Setenv("SERVER_ADDRESS", "https://www.api-route.com")
	t.Setenv("ZPAY_GATEWAY_URL", "")
	t.Setenv("ZPAY_MERCHANT_ID", "")
	t.Setenv("ZPAY_MERCHANT_ID_B64", base64.StdEncoding.EncodeToString([]byte("merchant-id")))
	t.Setenv("ZPAY_MERCHANT_KEY", "merchant-key")
	t.Setenv("STRIPE_SECRET_KEY", "stripe-secret")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("STRIPE_PRICE_ID", "price-id")
	t.Setenv("GITHUB_CLIENT_ID", "github-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "github-secret")

	applyEnvironmentOptionOverrides()

	assert.Equal(t, "https://www.api-route.com", system_setting.ServerAddress)
	assert.Equal(t, "https://zpayz.cn", operation_setting.PayAddress)
	assert.Equal(t, "merchant-id", operation_setting.EpayId)
	assert.Equal(t, "merchant-key", operation_setting.EpayKey)
	assert.Equal(t, "stripe-secret", setting.StripeApiSecret)
	assert.Equal(t, "webhook-secret", setting.StripeWebhookSecret)
	assert.Equal(t, "price-id", setting.StripePriceId)
	assert.Equal(t, "github-id", common.GitHubClientId)
	assert.Equal(t, "github-secret", common.GitHubClientSecret)
	assert.True(t, common.GitHubOAuthEnabled)
	assert.NotContains(t, common.OptionMap, "StripeApiSecret")
	assert.NotContains(t, common.OptionMap, "EpayKey")
	assert.NotContains(t, common.OptionMap, "GitHubClientSecret")
}

func TestGetSecretEnvPrefersPlaintextAndSupportsBase64(t *testing.T) {
	t.Setenv("NEW_API_TEST_SECRET", "")
	t.Setenv("NEW_API_TEST_SECRET_B64", base64.StdEncoding.EncodeToString([]byte("encoded-secret")))
	assert.Equal(t, "encoded-secret", common.GetSecretEnv("NEW_API_TEST_SECRET"))

	t.Setenv("NEW_API_TEST_SECRET", "plain-secret")
	assert.Equal(t, "plain-secret", common.GetSecretEnv("NEW_API_TEST_SECRET"))
}
