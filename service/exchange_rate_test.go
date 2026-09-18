package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetUSDExchangeRate(t *testing.T) {
	originalURL := exchangeRateURL
	originalClient := exchangeRateClient
	exchangeRateCache.Lock()
	originalRates := exchangeRateCache.rates
	originalUpdatedAt := exchangeRateCache.updatedAt
	exchangeRateCache.Unlock()
	t.Cleanup(func() {
		exchangeRateURL = originalURL
		exchangeRateClient = originalClient
		exchangeRateCache.Lock()
		exchangeRateCache.rates = originalRates
		exchangeRateCache.updatedAt = originalUpdatedAt
		exchangeRateCache.Unlock()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"date":"2026-09-18","base":"USD","quote":"CNY","rate":7.12},
			{"date":"2026-09-18","base":"USD","quote":"EUR","rate":0.84},
			{"date":"2026-09-18","base":"USD","quote":"BAD","rate":-1}
		]`))
	}))
	defer server.Close()
	exchangeRateURL = server.URL
	exchangeRateClient = server.Client()
	exchangeRateCache.Lock()
	exchangeRateCache.rates = map[string]float64{"USD": 1}
	exchangeRateCache.updatedAt = time.Time{}
	exchangeRateCache.Unlock()

	assert.Equal(t, 7.12, GetUSDExchangeRate(context.Background(), "cny", 7.3))
	assert.Equal(t, 0.84, GetUSDExchangeRate(context.Background(), "EUR", 1))
	assert.Equal(t, 1.0, GetUSDExchangeRate(context.Background(), "USD", 9))
	assert.Equal(t, 2.0, GetUSDExchangeRate(context.Background(), "XXX", 2))

	server.Close()
	exchangeRateCache.Lock()
	exchangeRateCache.updatedAt = time.Time{}
	exchangeRateCache.Unlock()
	assert.Equal(t, 7.12, GetUSDExchangeRate(context.Background(), "CNY", 7.3))
}
