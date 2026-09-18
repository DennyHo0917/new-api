package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetUSDCNYExchangeRate(t *testing.T) {
	originalURL := exchangeRateURL
	originalClient := exchangeRateClient
	exchangeRateCache.Lock()
	originalRate := exchangeRateCache.rate
	originalUpdatedAt := exchangeRateCache.updatedAt
	exchangeRateCache.Unlock()
	t.Cleanup(func() {
		exchangeRateURL = originalURL
		exchangeRateClient = originalClient
		exchangeRateCache.Lock()
		exchangeRateCache.rate = originalRate
		exchangeRateCache.updatedAt = originalUpdatedAt
		exchangeRateCache.Unlock()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"date":"2026-09-18","base":"USD","quote":"CNY","rate":7.12}
		]`))
	}))
	defer server.Close()
	exchangeRateURL = server.URL
	exchangeRateClient = server.Client()
	exchangeRateCache.Lock()
	exchangeRateCache.rate = 0
	exchangeRateCache.updatedAt = time.Time{}
	exchangeRateCache.Unlock()

	assert.Equal(t, 7.12, GetUSDCNYExchangeRate(context.Background(), 7.3))

	server.Close()
	exchangeRateCache.Lock()
	exchangeRateCache.updatedAt = time.Time{}
	exchangeRateCache.Unlock()
	assert.Equal(t, 7.12, GetUSDCNYExchangeRate(context.Background(), 7.3))
}
