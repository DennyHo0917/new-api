package operation_setting

import (
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

type CryptoSetting struct {
	EnableCrypto        bool                         `json:"enable_crypto"`
	CryptoExpiryMinutes int                          `json:"crypto_expiry_minutes"`
	CryptoMinTopUp      float64                      `json:"crypto_min_topup"`
	CryptoWallets       map[string]string            `json:"crypto_wallets"`
	ContractAddresses   map[string]map[string]string `json:"contract_addresses"`
	ArbitrumRpcUrl      string                       `json:"arbitrum_rpc_url"`
	TronGridApiKey      string                       `json:"trongrid_api_key"`
}

const (
	CryptoPaymentWindowMinutes = 10
	CryptoOrderExpiryMinutes   = 15
)

var cryptoSetting = CryptoSetting{
	EnableCrypto:        false,
	CryptoExpiryMinutes: CryptoOrderExpiryMinutes,
	CryptoMinTopUp:      5.0,
	CryptoWallets: map[string]string{
		"tron": "TV8FyJ72SmYr8zJVvhWKqFKEa1yDLgVBgv",
		"arb":  "0x56a3ff56e79394e34834188a6e83a5d219984eb2",
	},
	ContractAddresses: map[string]map[string]string{
		"tron": {
			"USDT": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		},
		"arb": {
			"USDT": "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9",
			"USDC": "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
		},
	},
	ArbitrumRpcUrl: "https://arb1.arbitrum.io/rpc",
	TronGridApiKey: "",
}

func init() {
	if env := os.Getenv("CRYPTO_ENABLE"); env != "" {
		cryptoSetting.EnableCrypto = strings.ToLower(env) == "true" || env == "1"
	}
	if env := os.Getenv("CRYPTO_TRON_WALLET"); env != "" {
		cryptoSetting.CryptoWallets["tron"] = strings.TrimSpace(env)
	}
	if env := os.Getenv("CRYPTO_ARB_WALLET"); env != "" {
		cryptoSetting.CryptoWallets["arb"] = strings.TrimSpace(env)
	}
	if env := os.Getenv("CRYPTO_ARBITRUM_RPC"); env != "" {
		cryptoSetting.ArbitrumRpcUrl = strings.TrimSpace(env)
	}
	if env := os.Getenv("CRYPTO_TRONGRID_KEY"); env != "" {
		cryptoSetting.TronGridApiKey = strings.TrimSpace(env)
	}
	if env := os.Getenv("CRYPTO_MIN_TOPUP"); env != "" {
		if val, err := strconv.ParseFloat(env, 64); err == nil && val > 0 {
			cryptoSetting.CryptoMinTopUp = val
		}
	}

	config.GlobalConfig.Register("crypto_setting", &cryptoSetting)
}

func GetCryptoSetting() *CryptoSetting {
	return &cryptoSetting
}

func (s *CryptoSetting) GetWalletAddress(chain string) string {
	if s.CryptoWallets == nil {
		return ""
	}
	return s.CryptoWallets[strings.ToLower(chain)]
}

func (s *CryptoSetting) GetContractAddress(chain, token string) string {
	if s.ContractAddresses == nil {
		return ""
	}
	chainMap := s.ContractAddresses[strings.ToLower(chain)]
	if chainMap == nil {
		return ""
	}
	return chainMap[strings.ToUpper(token)]
}
