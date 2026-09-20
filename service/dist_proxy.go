package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
)

type legacyQuotaMarkerKey struct{}

// ProxyToSubRouter forwards the current request and reports whether the
// upstream response was normalized as a legacy-quota exhaustion response.
func ProxyToSubRouter(c *gin.Context) bool {
	legacyQuotaExhausted := false
	requestContext := context.WithValue(c.Request.Context(), legacyQuotaMarkerKey{}, &legacyQuotaExhausted)
	c.Request = c.Request.WithContext(requestContext)
	logger.LogInfo(c, fmt.Sprintf("[SubRouter Proxy] Forwarding %s %s to SubRouter upstream", c.Request.Method, c.Request.URL.Path))
	proxy := GetSubRouterReverseProxy()
	proxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
	return legacyQuotaExhausted
}

var (
	subRouterProxyOnce sync.Once
	subRouterProxy     *httputil.ReverseProxy

	subRouterTransport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: false},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second, // Long LLM generation timeout
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
	}
)

const (
	DefaultSubRouterBaseURL = "https://apiroute.subrouter.ai"
	QuotaExhaustedMessage   = "您在旧平台的额度已耗尽，请前往 https://www.api-route.com/topup 充值以继续使用。"
)

// GetSubRouterBaseURL returns the configured or default SubRouter upstream URL
func GetSubRouterBaseURL() string {
	baseURL := strings.TrimSpace(os.Getenv("SUBROUTER_BASE_URL"))
	if baseURL == "" {
		baseURL = DefaultSubRouterBaseURL
	}
	return strings.TrimRight(baseURL, "/")
}

// GetSubRouterReverseProxy returns the singleton reverse proxy instance
func GetSubRouterReverseProxy() *httputil.ReverseProxy {
	subRouterProxyOnce.Do(func() {
		targetURL, err := url.Parse(GetSubRouterBaseURL())
		if err != nil {
			common.SysError(fmt.Sprintf("Failed to parse SubRouter target URL: %v", err))
			targetURL, _ = url.Parse(DefaultSubRouterBaseURL)
		}

		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.Transport = subRouterTransport
		proxy.FlushInterval = -1 // Flush immediately for SSE streaming!

		origDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			origDirector(req)
			req.Host = targetURL.Host // Required for Cloudflare / SNI / VirtualHost routing
			req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
		}

		// Intercept only explicit quota-exhaustion responses. A generic 429 can
		// be temporary rate limiting and must not switch a legacy key to local quota.
		proxy.ModifyResponse = func(resp *http.Response) error {
			var legacyQuotaExhausted *bool
			if resp.Request != nil {
				legacyQuotaExhausted, _ = resp.Request.Context().Value(legacyQuotaMarkerKey{}).(*bool)
			}
			if resp.StatusCode == http.StatusBadRequest ||
				resp.StatusCode == http.StatusPaymentRequired ||
				resp.StatusCode == http.StatusForbidden ||
				resp.StatusCode == http.StatusTooManyRequests {
				bodyBytes, err := io.ReadAll(resp.Body)
				if err == nil {
					bodyStr := strings.ToLower(string(bodyBytes))
					quotaExhausted := false
					for _, marker := range []string{
						"insufficient_user_quota",
						"insufficient_quota",
						"insufficient user quota",
						"user quota not enough",
						"quota not enough",
						"quota exhausted",
						"exceeded your current quota",
						"insufficient balance",
						"balance not enough",
						"额度不足",
						"额度已耗尽",
						"余额不足",
					} {
						if strings.Contains(bodyStr, marker) {
							quotaExhausted = true
							break
						}
					}
					if quotaExhausted {
						if legacyQuotaExhausted != nil {
							*legacyQuotaExhausted = true
						}
						_ = resp.Body.Close()
						errPayload := fmt.Sprintf(`{"error":{"message":"%s","type":"insufficient_quota","param":"","code":"insufficient_user_quota"}}`, QuotaExhaustedMessage)
						resp.StatusCode = http.StatusTooManyRequests
						resp.Status = "429 Too Many Requests"
						resp.Header.Set("Content-Type", "application/json; charset=utf-8")
						resp.Header.Del("Content-Encoding")
						resp.ContentLength = int64(len(errPayload))
						resp.Header.Set("Content-Length", strconv.Itoa(len(errPayload)))
						resp.Body = io.NopCloser(strings.NewReader(errPayload))
						return nil
					}
					resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				}
			}

			return nil
		}

		proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
			common.SysError(fmt.Sprintf("SubRouter reverse proxy error: %v", err))
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			rw.WriteHeader(http.StatusBadGateway)
			_, _ = rw.Write([]byte(`{"error":{"message":"上游网关连接失败，请稍后重试","type":"bad_gateway","code":"subrouter_upstream_error"}}`))
		}

		subRouterProxy = proxy
	})
	return subRouterProxy
}
