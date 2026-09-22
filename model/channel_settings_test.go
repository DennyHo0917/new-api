package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChannelValidateSettingsRejectsInvalidHTTPTransport(t *testing.T) {
	tests := []struct {
		name    string
		setting dto.ChannelSettings
		wantErr string
	}{
		{
			name:    "auto with shards is valid",
			setting: dto.ChannelSettings{HTTPProtocol: "auto", HTTP2ConnectionShards: 4},
		},
		{
			name:    "http1 with shards greater than one rejected",
			setting: dto.ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2},
			wantErr: "http2_connection_shards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{}
			channel.SetSetting(tt.setting)
			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestChannelModelGroupsCreateExactAbilities(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "model-groups.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&Ability{}))

	channel := &Channel{
		Id:     42,
		Models: "chat-model,image-model",
		Group:  "default,premium",
		Status: common.ChannelStatusEnabled,
	}
	channel.SetOtherSettings(dto.ChannelOtherSettings{ModelGroups: map[string][]string{
		"chat-model":  {"default"},
		"image-model": {"premium"},
	}})
	require.NoError(t, channel.ValidateSettings())
	require.NoError(t, channel.AddAbilities(db))

	var abilities []Ability
	require.NoError(t, db.Order("model").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	assert.Equal(t, "default", abilities[0].Group)
	assert.Equal(t, "chat-model", abilities[0].Model)
	assert.Equal(t, "premium", abilities[1].Group)
	assert.Equal(t, "image-model", abilities[1].Model)
}

func TestChannelGroupPostgresTextMigration(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	for _, tt := range []struct {
		name   string
		legacy bool
	}{
		{name: "fresh"},
		{name: "upgrade", legacy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx := db.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })

			schema := fmt.Sprintf("channel_group_migration_%d", time.Now().UnixNano())
			require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
			require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
			if tt.legacy {
				require.NoError(t, tx.Exec(`CREATE TABLE channels (id integer PRIMARY KEY, "group" varchar(64) DEFAULT 'default')`).Error)
				require.NoError(t, tx.Exec(`INSERT INTO channels (id, "group") VALUES (1, 'grok standard')`).Error)
			}

			require.NoError(t, migrateChannelGroupToText(tx))
			if !tt.legacy {
				require.NoError(t, tx.AutoMigrate(&Channel{}))
			}
			require.NoError(t, migrateChannelGroupToText(tx), "migration must be idempotent")

			var dataType string
			require.NoError(t, tx.Raw(`SELECT data_type FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'channels' AND column_name = 'group'`).Scan(&dataType).Error)
			assert.Equal(t, "text", dataType)

			longGroups := strings.Repeat("standard,enterprise,", 5)
			if tt.legacy {
				require.NoError(t, tx.Exec(`UPDATE channels SET "group" = ? WHERE id = 1`, longGroups).Error)
				var saved string
				require.NoError(t, tx.Raw(`SELECT "group" FROM channels WHERE id = 1`).Scan(&saved).Error)
				assert.Equal(t, longGroups, saved)
				return
			}
			require.NoError(t, tx.Create(&Channel{Id: 1, Key: "test", Name: "test", Group: longGroups}).Error)
		})
	}
}

func TestChannelModelGroupsRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name        string
		modelGroups map[string][]string
		wantErr     string
	}{
		{name: "unknown model", modelGroups: map[string][]string{"missing": {"default"}}, wantErr: "unknown model"},
		{name: "unknown group", modelGroups: map[string][]string{"chat-model": {"missing"}}, wantErr: "unknown group"},
		{name: "empty group list", modelGroups: map[string][]string{"chat-model": {}}, wantErr: "at least one group"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Models: "chat-model", Group: "default"}
			channel.SetOtherSettings(dto.ChannelOtherSettings{ModelGroups: tt.modelGroups})
			err := channel.ValidateSettings()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestChannelModelMultipliersValidateModelAndValue(t *testing.T) {
	for _, test := range []struct {
		name       string
		multiplier map[string]float64
		wantError  string
	}{
		{name: "valid", multiplier: map[string]float64{"chat-model": 0.7}},
		{name: "free", multiplier: map[string]float64{"chat-model": 0}},
		{name: "unknown model", multiplier: map[string]float64{"missing": 1}, wantError: "unknown model"},
		{name: "negative", multiplier: map[string]float64{"chat-model": -1}, wantError: "invalid multiplier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			channel := &Channel{Models: "chat-model", Group: "default"}
			channel.SetOtherSettings(dto.ChannelOtherSettings{ModelMultipliers: test.multiplier})
			err := channel.ValidateSettings()
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestAdvancedCustomChannelRequiresModelListRouteOnlyWhenUpdateChecksEnabled(t *testing.T) {
	inferenceRoute := dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
		Converter:    "none",
	}

	tests := []struct {
		name          string
		checksEnabled bool
		routes        []dto.AdvancedCustomRoute
		wantErr       string
	}{
		{
			name:   "legacy channel without discovery route remains valid",
			routes: []dto.AdvancedCustomRoute{inferenceRoute},
		},
		{
			name:          "enabled checks require discovery route",
			checksEnabled: true,
			routes:        []dto.AdvancedCustomRoute{inferenceRoute},
			wantErr:       dto.AdvancedCustomModelListPath,
		},
		{
			name:          "enabled checks accept discovery route",
			checksEnabled: true,
			routes: []dto.AdvancedCustomRoute{
				inferenceRoute,
				{
					IncomingPath: dto.AdvancedCustomModelListPath,
					UpstreamPath: dto.AdvancedCustomModelListPath,
					Converter:    "none",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeAdvancedCustom}
			channel.SetOtherSettings(dto.ChannelOtherSettings{
				UpstreamModelUpdateCheckEnabled: tt.checksEnabled,
				AdvancedCustom: &dto.AdvancedCustomConfig{
					Routes: tt.routes,
				},
			})

			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInferencePresetSettingsAndDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "presets.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			table := db.Table("inference_preset_channels").Session(&gorm.Session{})
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("inference_preset_channels")) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			for _, channelType := range []int{constant.ChannelTypeVLLM, constant.ChannelTypeSGLang} {
				t.Run(fmt.Sprint(channelType), func(t *testing.T) {
					channel := &Channel{Type: channelType, Key: "EMPTY", Name: "inference", Status: common.ChannelStatusEnabled}
					require.NoError(t, channel.ValidateSettings())
					require.NotNil(t, channel.GetOtherSettings().AdvancedCustom)
					require.NoError(t, table.Create(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						assert.Equal(t, channelType, loaded.Type)
						assert.Empty(t, loaded.OtherSettings)
						defaults := loaded.GetOtherSettings().AdvancedCustom
						require.NotNil(t, defaults)
						assert.True(t, defaults.SupportsPath("/v1/messages"))
						assert.Empty(t, loaded.OtherSettings, "reading defaults must not rewrite saved settings")
						defaults.Routes[0].UpstreamPath = "/changed-locally"
						assert.Equal(t, "/v1/chat/completions", loaded.GetOtherSettings().AdvancedCustom.Routes[0].UpstreamPath)
					}
					settings := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/chat", Models: []string{"allowed"}, Auth: &dto.AdvancedCustomRouteAuth{Type: "none"}}}}}
					channel.SetOtherSettings(settings)
					require.NoError(t, channel.ValidateSettings())
					require.NoError(t, table.Save(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						actual := loaded.GetOtherSettings().AdvancedCustom
						require.Equal(t, common.GetAdvancedCustomPreset(channelType), actual, "named channels must ignore editable advanced_custom overrides")
						assert.Equal(t, channel.OtherSettings, loaded.OtherSettings)
						for _, tc := range []struct {
							path, model string
							allowed     bool
						}{
							{"/v1/chat/completions", "allowed", true},
							{"/v1/chat/completions", "other", true},
							{"/v1/messages", "allowed", true},
							{"/v1/images/generations", "allowed", false},
						} {
							ok, _ := ChannelSatisfiesFilters(&loaded, tc.model, []filterdto.ChannelFilter{{Kind: filterdto.FilterRequestPath, RequestPath: tc.path}})
							assert.Equal(t, tc.allowed, ok)
						}
						endpoints := getPricingEndpointTypesForAbility(AbilityWithChannel{ChannelType: channelType, Ability: Ability{Model: "allowed", ChannelId: loaded.Id}}, map[int]*dto.AdvancedCustomConfig{loaded.Id: actual})
						expectedEndpoints := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings}
						if channelType == constant.ChannelTypeSGLang {
							expectedEndpoints = append(expectedEndpoints, constant.EndpointTypeJinaRerank)
						}
						assert.ElementsMatch(t, expectedEndpoints, endpoints)
					}
				})
			}
		})
	}
}
