package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const exchangeRateRefreshInterval = 24 * time.Hour

var (
	exchangeRateURL    = "https://api.frankfurter.dev/v2/rates?base=USD&quotes=CNY"
	exchangeRateClient = &http.Client{Timeout: 10 * time.Second}
	exchangeRateCache  = struct {
		sync.RWMutex
		rate      float64
		updatedAt time.Time
	}{}
	exchangeRateRefreshMu sync.Mutex
)

type exchangeRateRow struct {
	Quote string  `json:"quote"`
	Rate  float64 `json:"rate"`
}

// GetUSDCNYExchangeRate returns the number of CNY per USD.
// The configured fallback keeps checkout available during a cold-start provider outage.
func GetUSDCNYExchangeRate(ctx context.Context, fallback float64) float64 {
	exchangeRateCache.RLock()
	rate := exchangeRateCache.rate
	stale := time.Since(exchangeRateCache.updatedAt) >= exchangeRateRefreshInterval
	exchangeRateCache.RUnlock()
	if rate > 0 && !stale {
		return rate
	}

	if err := refreshExchangeRates(ctx); err != nil {
		common.SysError(fmt.Sprintf("refresh exchange rates failed: %v", err))
	}
	exchangeRateCache.RLock()
	rate = exchangeRateCache.rate
	exchangeRateCache.RUnlock()
	if rate > 0 {
		return rate
	}
	if fallback > 0 && !math.IsNaN(fallback) && !math.IsInf(fallback, 0) {
		return fallback
	}
	return 1
}

func refreshExchangeRates(ctx context.Context) error {
	exchangeRateRefreshMu.Lock()
	defer exchangeRateRefreshMu.Unlock()

	exchangeRateCache.RLock()
	stale := time.Since(exchangeRateCache.updatedAt) >= exchangeRateRefreshInterval
	exchangeRateCache.RUnlock()
	if !stale {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exchangeRateURL, nil)
	if err != nil {
		return err
	}
	resp, err := exchangeRateClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}

	var rows []exchangeRateRow
	if err := common.DecodeJson(resp.Body, &rows); err != nil {
		return err
	}
	rate := 0.0
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Quote), "CNY") && row.Rate > 0 && !math.IsNaN(row.Rate) && !math.IsInf(row.Rate, 0) {
			rate = row.Rate
			break
		}
	}
	if rate == 0 {
		return errors.New("provider returned no valid USD/CNY rate")
	}

	exchangeRateCache.Lock()
	exchangeRateCache.rate = rate
	exchangeRateCache.updatedAt = time.Now()
	exchangeRateCache.Unlock()
	return nil
}

func StartExchangeRateRefreshTask() {
	go func() {
		if err := refreshExchangeRates(context.Background()); err != nil {
			common.SysError(fmt.Sprintf("initial exchange rate refresh failed: %v", err))
		}
		ticker := time.NewTicker(exchangeRateRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := refreshExchangeRates(context.Background()); err != nil {
				common.SysError(fmt.Sprintf("scheduled exchange rate refresh failed: %v", err))
			}
		}
	}()
}
