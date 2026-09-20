package oauth

import (
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

var (
	providers = make(map[string]Provider)
	mu        sync.RWMutex
	// customProviderSlugs tracks which providers are custom (can be unregistered)
	customProviderSlugs     = make(map[string]bool)
	customProviderConflicts = make(map[string]bool)
)

// Register registers an OAuth provider with the given name
func Register(name string, provider Provider) {
	mu.Lock()
	defer mu.Unlock()
	providers[name] = provider
}

// RegisterCustom registers a custom OAuth provider (can be unregistered later)
func RegisterCustom(name string, provider Provider) error {
	mu.Lock()
	defer mu.Unlock()
	if providers[name] != nil && !customProviderSlugs[name] {
		customProviderConflicts[name] = true
		return fmt.Errorf("custom OAuth provider %q conflicts with a built-in provider; rename the custom provider", name)
	}
	providers[name] = provider
	customProviderSlugs[name] = true
	return nil
}

func HasCustomProviderConflict(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return customProviderConflicts[name]
}

// Unregister removes a provider from the registry
func Unregister(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(providers, name)
	delete(customProviderSlugs, name)
}

// GetProvider returns the OAuth provider for the given name
func GetProvider(name string) Provider {
	mu.RLock()
	defer mu.RUnlock()
	return providers[name]
}

// GetAllProviders returns all registered OAuth providers
func GetAllProviders() map[string]Provider {
	mu.RLock()
	defer mu.RUnlock()
	result := make(map[string]Provider, len(providers))
	maps.Copy(result, providers)
	return result
}

// GetEnabledCustomProviders returns all enabled custom OAuth providers
func GetEnabledCustomProviders() []*GenericOAuthProvider {
	mu.RLock()
	defer mu.RUnlock()
	var result []*GenericOAuthProvider
	for name, provider := range providers {
		if customProviderSlugs[name] {
			if gp, ok := provider.(*GenericOAuthProvider); ok && gp.IsEnabled() {
				result = append(result, gp)
			}
		}
	}
	return result
}

// IsProviderRegistered checks if a provider is registered
func IsProviderRegistered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := providers[name]
	return ok
}

// IsCustomProvider checks if a provider is a custom provider
func IsCustomProvider(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return customProviderSlugs[name]
}

// LoadCustomProviders loads all custom OAuth providers from the database
func LoadCustomProviders() error {
	// First, unregister all existing custom providers
	mu.Lock()
	for name := range customProviderSlugs {
		delete(providers, name)
	}
	customProviderSlugs = make(map[string]bool)
	customProviderConflicts = make(map[string]bool)
	mu.Unlock()

	// Load all custom providers from database
	customProviders, err := model.GetAllCustomOAuthProviders()
	if err != nil {
		common.SysError("Failed to load custom OAuth providers: " + err.Error())
		return err
	}

	// Register each custom provider
	var conflict error
	for _, config := range customProviders {
		config = environmentProviderConfig(config)
		provider := NewGenericOAuthProvider(config)
		if err := RegisterCustom(config.Slug, provider); err != nil {
			common.SysError(err.Error())
			conflict = err
			continue
		}
		common.SysLog("Loaded custom OAuth provider: " + config.Name + " (" + config.Slug + ")")
	}

	common.SysLog(fmt.Sprintf("Loaded %d custom OAuth providers", len(customProviders)))
	return conflict
}

// PrepareEnvironmentProviders creates only the non-secret provider metadata
// needed for stable user binding IDs. Credentials remain in environment
// variables and are applied to the in-memory provider configuration.
func PrepareEnvironmentProviders() error {
	for _, config := range []*model.CustomOAuthProvider{
		environmentProviderConfig(&model.CustomOAuthProvider{Slug: "google"}),
		environmentProviderConfig(&model.CustomOAuthProvider{Slug: "x"}),
	} {
		if !config.Enabled {
			continue
		}
		if _, err := model.GetCustomOAuthProviderBySlug(config.Slug); err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		persisted := *config
		persisted.ClientSecret = ""
		if err := model.CreateCustomOAuthProvider(&persisted); err != nil {
			return err
		}
	}
	return nil
}

func environmentProviderConfig(config *model.CustomOAuthProvider) *model.CustomOAuthProvider {
	copy := *config
	switch copy.Slug {
	case "google":
		clientID := common.GetSecretEnv("GOOGLE_CLIENT_ID")
		clientSecret := common.GetSecretEnv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			if copy.ClientSecret == "" {
				copy.Enabled = false
			}
			return &copy
		}
		copy.Name = "Google"
		copy.Enabled = true
		copy.ClientId = clientID
		copy.ClientSecret = clientSecret
		copy.AuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
		copy.TokenEndpoint = "https://oauth2.googleapis.com/token"
		copy.UserInfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
		copy.Scopes = "openid email profile"
		copy.UserIdField = "sub"
		copy.UsernameField = "preferred_username"
		copy.DisplayNameField = "name"
		copy.EmailField = "email"
		copy.AuthStyle = AuthStyleInParams
	case "x":
		clientID := common.GetSecretEnv("X_CLIENT_ID")
		clientSecret := common.GetSecretEnv("X_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			if copy.ClientSecret == "" {
				copy.Enabled = false
			}
			return &copy
		}
		copy.Name = "X"
		copy.Enabled = true
		copy.ClientId = clientID
		copy.ClientSecret = clientSecret
		copy.AuthorizationEndpoint = "https://x.com/i/oauth2/authorize"
		copy.TokenEndpoint = "https://api.x.com/2/oauth2/token"
		copy.UserInfoEndpoint = "https://api.x.com/2/users/me?user.fields=id,name,username,profile_image_url"
		copy.Scopes = "tweet.read users.read"
		copy.UserIdField = "data.id"
		copy.UsernameField = "data.username"
		copy.DisplayNameField = "data.name"
		copy.EmailField = "data.email"
		copy.AuthStyle = AuthStyleInHeader
	}
	return &copy
}

// ReloadCustomProviders reloads all custom OAuth providers from the database
func ReloadCustomProviders() error {
	return LoadCustomProviders()
}

// RegisterOrUpdateCustomProvider registers or updates a single custom provider
func RegisterOrUpdateCustomProvider(config *model.CustomOAuthProvider) {
	provider := NewGenericOAuthProvider(config)
	if err := RegisterCustom(config.Slug, provider); err != nil {
		common.SysError(err.Error())
	}
}

// UnregisterCustomProvider unregisters a custom provider by slug
func UnregisterCustomProvider(slug string) {
	mu.Lock()
	defer mu.Unlock()
	if customProviderSlugs[slug] {
		delete(providers, slug)
		delete(customProviderSlugs, slug)
	}
	delete(customProviderConflicts, slug)
}
