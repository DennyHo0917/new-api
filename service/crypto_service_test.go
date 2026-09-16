package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyArbitrumReceipt(t *testing.T) {
	targetContract := "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9" // Arb USDT
	targetWallet := "0x1234567890abcdef1234567890abcdef12345678"

	// 1. Success case: Transfer 25.5 USDT (25,500,000 = 0x1851960)
	receiptSuccess := &EthReceiptResult{
		TransactionHash: "0xabc123",
		Status:          "0x1",
		Logs: []EthLog{
			{
				Address: targetContract,
				Topics: []string{
					TransferEventSignature,
					"0x0000000000000000000000001122334455667788990011223344556677889900",
					"0x0000000000000000000000001234567890abcdef1234567890abcdef12345678",
				},
				Data: "0x0000000000000000000000000000000000000000000000000000000001851960",
			},
		},
	}

	from, amount, err := VerifyArbitrumReceipt(receiptSuccess, targetContract, targetWallet)
	require.NoError(t, err)
	assert.Equal(t, 25.5, amount)
	assert.Equal(t, "0x1122334455667788990011223344556677889900", from)

	// 2. Failed status (0x0)
	receiptFailed := &EthReceiptResult{
		TransactionHash: "0xfailed",
		Status:          "0x0",
		Logs:            receiptSuccess.Logs,
	}
	_, _, err = VerifyArbitrumReceipt(receiptFailed, targetContract, targetWallet)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction failed on-chain")

	// 3. Wrong recipient wallet
	_, _, err = VerifyArbitrumReceipt(receiptSuccess, targetContract, "0x9999999999999999999999999999999999999999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matching Transfer event to platform wallet not found")

	// 4. Wrong token contract
	_, _, err = VerifyArbitrumReceipt(receiptSuccess, "0x0000000000000000000000000000000000000001", targetWallet)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matching Transfer event to platform wallet not found")
}

func TestVerifyTronEvents(t *testing.T) {
	targetContract := "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	targetWallet := "TYPz7d8c4K8Yj772qZ7Nf5k2Z8w9abc123"

	// 1. Success case: Transfer 100 USDT (100,000,000 units)
	events := []TronGridEventItem{
		{
			ContractAddress: targetContract,
			EventName:       "Transfer",
			Result: map[string]string{
				"from":  "TFromAddress1234567890",
				"to":    targetWallet,
				"value": "100000000",
			},
		},
	}

	from, amount, err := VerifyTronEvents(events, targetContract, targetWallet)
	require.NoError(t, err)
	assert.Equal(t, 100.0, amount)
	assert.Equal(t, "TFromAddress1234567890", from)

	// 2. Target wallet mismatch
	_, _, err = VerifyTronEvents(events, targetContract, "TOtherWalletXYZ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matching TRC20 Transfer event to platform wallet not found")
}
