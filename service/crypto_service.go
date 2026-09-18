package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

const (
	TransferEventSignature = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	TransferTopicNo0x      = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var cryptoHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
}

type EthRpcRequest struct {
	Jsonrpc string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	Id      int    `json:"id"`
}

type EthLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type EthReceiptResult struct {
	TransactionHash string   `json:"transactionHash"`
	Status          string   `json:"status"`
	Logs            []EthLog `json:"logs"`
}

type EthReceiptResponse struct {
	Jsonrpc string            `json:"jsonrpc"`
	Id      int               `json:"id"`
	Result  *EthReceiptResult `json:"result"`
	Error   any               `json:"error,omitempty"`
}

type TronGridEventItem struct {
	ContractAddress string            `json:"contract_address"`
	EventName       string            `json:"event_name"`
	Result          map[string]string `json:"result"`
}

type TronGridEventResponse struct {
	Success bool                `json:"success"`
	Data    []TronGridEventItem `json:"data"`
}

type TronTxInfoLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type TronReceipt struct {
	Result string `json:"result"`
}

type TronTxInfoResponse struct {
	Id      string          `json:"id"`
	Receipt TronReceipt     `json:"receipt"`
	Log     []TronTxInfoLog `json:"log"`
	Result  string          `json:"result"`
}

func decodeTronAddress(address string) (string, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	clean := strings.TrimSpace(address)
	if clean == "" {
		return "", errors.New("empty tron address")
	}

	value := new(big.Int)
	base := big.NewInt(58)
	for _, char := range clean {
		index := strings.IndexRune(alphabet, char)
		if index < 0 {
			return "", fmt.Errorf("invalid tron address character")
		}
		value.Mul(value, base)
		value.Add(value, big.NewInt(int64(index)))
	}

	decoded := value.Bytes()
	for len(decoded) < 25 {
		decoded = append([]byte{0}, decoded...)
	}
	if len(decoded) != 25 || decoded[0] != 0x41 {
		return "", errors.New("invalid tron address payload")
	}
	first := sha256.Sum256(decoded[:21])
	second := sha256.Sum256(first[:])
	if !bytes.Equal(decoded[21:], second[:4]) {
		return "", errors.New("invalid tron address checksum")
	}
	return hex.EncodeToString(decoded[1:21]), nil
}

// VerifyArbitrumReceipt verifies an EVM transaction receipt from Arbitrum One.
func VerifyArbitrumReceipt(receipt *EthReceiptResult, targetContract string, targetWallet string) (string, float64, error) {
	if receipt == nil {
		return "", 0, errors.New("transaction receipt is empty or unconfirmed")
	}

	if receipt.Status != "0x1" {
		return "", 0, errors.New("transaction failed on-chain (status is not 0x1)")
	}

	cleanTargetWallet := strings.ToLower(strings.TrimPrefix(targetWallet, "0x"))
	cleanTargetContract := strings.ToLower(strings.TrimPrefix(targetContract, "0x"))

	for _, l := range receipt.Logs {
		logContract := strings.ToLower(strings.TrimPrefix(l.Address, "0x"))
		if cleanTargetContract != "" && logContract != cleanTargetContract {
			continue
		}

		if len(l.Topics) < 3 {
			continue
		}

		topic0 := strings.ToLower(l.Topics[0])
		if topic0 != strings.ToLower(TransferEventSignature) && topic0 != TransferTopicNo0x {
			continue
		}

		toTopic := strings.ToLower(strings.TrimPrefix(l.Topics[2], "0x"))
		if len(toTopic) >= 40 {
			toTopic = toTopic[len(toTopic)-40:]
		}

		if cleanTargetWallet != "" && toTopic != cleanTargetWallet {
			continue
		}

		fromTopic := strings.ToLower(strings.TrimPrefix(l.Topics[1], "0x"))
		if len(fromTopic) >= 40 {
			fromTopic = fromTopic[len(fromTopic)-40:]
		}
		fromAddress := "0x" + fromTopic

		dataStr := strings.TrimPrefix(l.Data, "0x")
		if dataStr == "" {
			continue
		}
		amountBig := new(big.Int)
		if _, ok := amountBig.SetString(dataStr, 16); !ok {
			return "", 0, fmt.Errorf("invalid token transfer data: %s", l.Data)
		}

		// USDT and USDC on Arbitrum have 6 decimals
		divisor := big.NewInt(1000000)
		amountFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(amountBig), new(big.Float).SetInt(divisor)).Float64()

		return fromAddress, amountFloat, nil
	}

	return "", 0, errors.New("matching Transfer event to platform wallet not found in transaction logs")
}

// VerifyTronEvents parses TronGrid events for TRC20 Transfer.
func VerifyTronEvents(events []TronGridEventItem, targetContract string, targetWallet string) (string, float64, error) {
	for _, ev := range events {
		if ev.EventName != "Transfer" {
			continue
		}

		if targetContract != "" && !strings.EqualFold(ev.ContractAddress, targetContract) {
			continue
		}

		toAddr := ev.Result["to"]
		if targetWallet != "" && !strings.EqualFold(toAddr, targetWallet) {
			continue
		}

		fromAddr := ev.Result["from"]
		valStr := ev.Result["value"]
		valBig := new(big.Int)
		if _, ok := valBig.SetString(valStr, 10); !ok {
			return "", 0, fmt.Errorf("invalid transfer value: %s", valStr)
		}

		// TRC20 USDT has 6 decimals
		divisor := big.NewInt(1000000)
		amountFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(valBig), new(big.Float).SetInt(divisor)).Float64()

		return fromAddr, amountFloat, nil
	}

	return "", 0, errors.New("matching TRC20 Transfer event to platform wallet not found")
}

// VerifyTronTxInfo parses Tron gettransactioninfobyid RPC result.
func VerifyTronTxInfo(info *TronTxInfoResponse, targetContract string, targetWallet string) (string, float64, error) {
	if info == nil {
		return "", 0, errors.New("transaction info is empty or unconfirmed")
	}

	if info.Receipt.Result != "" && info.Receipt.Result != "SUCCESS" {
		return "", 0, fmt.Errorf("tron transaction execution failed: %s", info.Receipt.Result)
	}
	targetContractHex, err := decodeTronAddress(targetContract)
	if err != nil {
		return "", 0, fmt.Errorf("invalid target tron contract: %w", err)
	}
	targetWalletHex, err := decodeTronAddress(targetWallet)
	if err != nil {
		return "", 0, fmt.Errorf("invalid target tron wallet: %w", err)
	}

	for _, l := range info.Log {
		if len(l.Topics) < 3 {
			continue
		}

		topic0 := strings.ToLower(l.Topics[0])
		if topic0 != TransferTopicNo0x && topic0 != strings.ToLower(TransferEventSignature) {
			continue
		}
		logContract := strings.TrimPrefix(strings.ToLower(l.Address), "0x")
		if len(logContract) == 42 && strings.HasPrefix(logContract, "41") {
			logContract = logContract[2:]
		}
		if logContract != targetContractHex {
			continue
		}
		toTopic := strings.TrimPrefix(strings.ToLower(l.Topics[2]), "0x")
		if len(toTopic) < 40 || toTopic[len(toTopic)-40:] != targetWalletHex {
			continue
		}

		dataStr := strings.TrimPrefix(l.Data, "0x")
		if dataStr == "" {
			continue
		}
		amountBig := new(big.Int)
		if _, ok := amountBig.SetString(dataStr, 16); !ok {
			return "", 0, fmt.Errorf("invalid token transfer data: %s", l.Data)
		}

		divisor := big.NewInt(1000000)
		amountFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(amountBig), new(big.Float).SetInt(divisor)).Float64()

		fromTopic := strings.TrimPrefix(strings.ToLower(l.Topics[1]), "0x")
		if len(fromTopic) >= 40 {
			fromTopic = fromTopic[len(fromTopic)-40:]
		}

		return "41" + fromTopic, amountFloat, nil
	}

	return "", 0, errors.New("matching Transfer event not found in transaction info logs")
}

// QueryArbitrumTransaction queries Arbitrum RPC node for receipt.
func QueryArbitrumTransaction(ctx context.Context, txHash string, rpcUrl string, targetContract string, targetWallet string) (string, float64, error) {
	if rpcUrl == "" {
		rpcUrl = "https://arb1.arbitrum.io/rpc"
	}

	reqPayload := EthRpcRequest{
		Jsonrpc: "2.0",
		Method:  "eth_getTransactionReceipt",
		Params:  []any{txHash},
		Id:      1,
	}

	bodyBytes, err := common.Marshal(reqPayload)
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal eth request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcUrl, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create eth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cryptoHTTPClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("failed to query arbitrum rpc: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read arbitrum rpc response: %w", err)
	}

	var rpcResp EthReceiptResponse
	if err := common.Unmarshal(respBytes, &rpcResp); err != nil {
		return "", 0, fmt.Errorf("failed to unmarshal arbitrum rpc response: %w", err)
	}

	if rpcResp.Result == nil {
		return "", 0, errors.New("transaction receipt not found or still pending")
	}

	return VerifyArbitrumReceipt(rpcResp.Result, targetContract, targetWallet)
}

// QueryTronTransaction queries Tron network for transaction details.
func QueryTronTransaction(ctx context.Context, txHash string, apiKey string, targetContract string, targetWallet string) (string, float64, error) {
	// First try TronGrid events endpoint
	cleanTxHash := strings.TrimPrefix(txHash, "0x")
	eventsUrl := fmt.Sprintf("https://api.trongrid.io/v1/transactions/%s/events", cleanTxHash)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsUrl, nil)
	if err == nil {
		if apiKey != "" {
			req.Header.Set("TRON-PRO-API-KEY", apiKey)
		}
		resp, err := cryptoHTTPClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				bodyBytes, readErr := io.ReadAll(resp.Body)
				if readErr == nil {
					var eventResp TronGridEventResponse
					if err := common.Unmarshal(bodyBytes, &eventResp); err == nil && eventResp.Success && len(eventResp.Data) > 0 {
						from, amount, verifyErr := VerifyTronEvents(eventResp.Data, targetContract, targetWallet)
						if verifyErr == nil && amount > 0 {
							info, infoErr := queryTronTransactionInfo(ctx, cleanTxHash, apiKey)
							if infoErr == nil && (info.Receipt.Result == "" || info.Receipt.Result == "SUCCESS") {
								return from, amount, nil
							}
						}
					}
				}
			}
		}
	}

	// Fallback to wallet/gettransactioninfobyid
	infoResp, err := queryTronTransactionInfo(ctx, cleanTxHash, apiKey)
	if err != nil {
		return "", 0, err
	}
	return VerifyTronTxInfo(infoResp, targetContract, targetWallet)
}

func queryTronTransactionInfo(ctx context.Context, txHash string, apiKey string) (*TronTxInfoResponse, error) {
	infoUrl := "https://api.trongrid.io/wallet/gettransactioninfobyid"
	postData := map[string]string{"value": strings.TrimPrefix(txHash, "0x")}
	postBytes, err := common.Marshal(postData)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, infoUrl, bytes.NewReader(postBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", apiKey)
	}

	resp, err := cryptoHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query tron node: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tron node response: %w", err)
	}

	var infoResp TronTxInfoResponse
	if err := common.Unmarshal(respBytes, &infoResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tron node response: %w", err)
	}
	return &infoResp, nil
}

// VerifyAndSettleCryptoTx orchestrates the on-chain verification and atomic quota settlement.
func VerifyAndSettleCryptoTx(ctx context.Context, tradeNo string, txHash string) (bool, string, float64, error) {
	order, err := model.GetCryptoTransactionByTradeNo(tradeNo)
	if err != nil {
		return false, "", 0, fmt.Errorf("order not found: %w", err)
	}

	if order.Status == model.CryptoStatusSuccess {
		return true, "order already completed", order.ActualAmount, nil
	}

	if order.Status == model.CryptoStatusExpired {
		return false, "order expired", 0, errors.New("order is expired")
	}
	if order.IsExpired(time.Now().Unix()) {
		_ = model.ExpireCryptoTransaction(tradeNo, "订单超时未完成")
		return false, "order expired", 0, errors.New("order is expired")
	}

	cleanTxHash := strings.TrimSpace(txHash)
	if cleanTxHash == "" {
		return false, "invalid tx hash", 0, errors.New("tx hash is required")
	}

	// Prevent replay of txHash on any other orders
	existingOrder, err := model.GetCryptoTransactionByTxHash(cleanTxHash)
	if err == nil && existingOrder != nil && existingOrder.TradeNo != order.TradeNo {
		return false, "tx hash already used by another order", 0, errors.New("transaction hash has already been redeemed")
	}

	// Set order to processing and save txHash
	if err := model.SubmitCryptoTransactionTxHash(tradeNo, cleanTxHash); err != nil {
		return false, err.Error(), 0, err
	}

	cfg := operation_setting.GetCryptoSetting()
	chain := strings.ToLower(order.Chain)
	token := strings.ToUpper(order.Token)
	targetWallet := order.WalletAddress
	targetContract := cfg.GetContractAddress(chain, token)

	var fromAddress string
	var actualAmount float64
	var verifyErr error

	switch chain {
	case "arb", "arbitrum":
		fromAddress, actualAmount, verifyErr = QueryArbitrumTransaction(ctx, cleanTxHash, cfg.ArbitrumRpcUrl, targetContract, targetWallet)
	case "tron", "trc20":
		fromAddress, actualAmount, verifyErr = QueryTronTransaction(ctx, cleanTxHash, cfg.TronGridApiKey, targetContract, targetWallet)
	default:
		verifyErr = fmt.Errorf("unsupported blockchain: %s", order.Chain)
	}

	if verifyErr != nil {
		_ = model.FailCryptoTransaction(tradeNo, verifyErr.Error())
		return false, verifyErr.Error(), 0, verifyErr
	}

	if actualAmount <= 0 {
		reason := "actual transferred amount is 0"
		_ = model.FailCryptoTransaction(tradeNo, reason)
		return false, reason, 0, errors.New(reason)
	}

	quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	amountDec := decimal.NewFromFloat(actualAmount).Mul(quotaPerUnit)
	quotaInt, err := common.WalletQuotaFromDecimalStrict(amountDec)
	if err != nil {
		reason := "quota amount exceeds system representation limit"
		_ = model.FailCryptoTransaction(tradeNo, reason)
		return false, reason, actualAmount, err
	}

	// Settle order with actualAmount and quotaInt
	err = model.CompleteCryptoTransaction(tradeNo, actualAmount, int64(quotaInt), fromAddress, cleanTxHash)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to complete crypto order %s: %v", tradeNo, err))
		return false, "failed to credit quota", actualAmount, err
	}

	return true, "success", actualAmount, nil
}
