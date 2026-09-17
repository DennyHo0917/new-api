package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOIDCProvider_GetName(t *testing.T) {
	settings := system_setting.GetOIDCSettings()
	originalDisplayName := settings.DisplayName
	defer func() { settings.DisplayName = originalDisplayName }()

	p := &OIDCProvider{}

	settings.DisplayName = ""
	assert.Equal(t, "OIDC", p.GetName())

	settings.DisplayName = "  Acme SSO  "
	assert.Equal(t, "Acme SSO", p.GetName())
}

func TestGenericOAuthExchangeSendsPKCEVerifier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "pkce-verifier", r.Form.Get("code_verifier"))
		assert.Equal(t, "authorization-code", r.Form.Get("code"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer"}`))
	}))
	defer server.Close()

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:          "Test",
		Slug:          "test",
		ClientId:      "client-id",
		ClientSecret:  "client-secret",
		TokenEndpoint: server.URL,
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(OAuthCodeVerifierContextKey, "pkce-verifier")
	token, err := provider.ExchangeToken(context.Background(), "authorization-code", c)
	require.NoError(t, err)
	assert.Equal(t, "access-token", token.AccessToken)
}

func TestPrepareEnvironmentProvidersDoesNotPersistSecrets(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "google-client")
	t.Setenv("GOOGLE_CLIENT_SECRET", "google-secret")
	t.Setenv("X_CLIENT_ID", "x-client")
	t.Setenv("X_CLIENT_SECRET", "x-secret")

	for _, test := range []struct {
		name string
		dsn  string
		open func(string) gorm.Dialector
	}{
		{name: "sqlite", dsn: ":memory:", open: sqlite.Open},
		{name: "mysql", dsn: os.Getenv("TEST_MYSQL_DSN"), open: mysql.Open},
		{name: "postgres", dsn: os.Getenv("TEST_POSTGRES_DSN"), open: postgres.Open},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			previousDB := model.DB
			db, err := gorm.Open(test.open(test.dsn), &gorm.Config{})
			require.NoError(t, err)
			model.DB = db
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&model.CustomOAuthProvider{})
				if sqlDB, err := db.DB(); err == nil {
					_ = sqlDB.Close()
				}
				model.DB = previousDB
			})
			_ = db.Migrator().DropTable(&model.CustomOAuthProvider{})
			require.NoError(t, db.AutoMigrate(&model.CustomOAuthProvider{}))
			require.NoError(t, db.AutoMigrate(&model.CustomOAuthProvider{}))

			require.NoError(t, PrepareEnvironmentProviders())
			require.NoError(t, PrepareEnvironmentProviders())

			google, err := model.GetCustomOAuthProviderBySlug("google")
			require.NoError(t, err)
			assert.Empty(t, google.ClientSecret)
			assert.Equal(t, "google-client", google.ClientId)
			xProvider, err := model.GetCustomOAuthProviderBySlug("x")
			require.NoError(t, err)
			assert.Empty(t, xProvider.ClientSecret)
			assert.Equal(t, "x-client", xProvider.ClientId)
			var count int64
			require.NoError(t, db.Model(&model.CustomOAuthProvider{}).Count(&count).Error)
			assert.EqualValues(t, 2, count)
		})
	}
}

func TestEnvironmentManagedProviderDisablesWithoutRuntimeSecret(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	config := environmentProviderConfig(&model.CustomOAuthProvider{
		Slug: "google", Enabled: true, ClientId: "public-client-id",
	})
	assert.False(t, config.Enabled)
}
