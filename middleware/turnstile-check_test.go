package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type turnstileRoundTripFunc func(*http.Request) (*http.Response, error)

func (f turnstileRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTurnstileCheck(t *testing.T) {
	previousEnabled := common.TurnstileCheckEnabled
	previousSecret := common.TurnstileSecretKey
	previousClient := http.DefaultClient
	t.Cleanup(func() {
		common.TurnstileCheckEnabled = previousEnabled
		common.TurnstileSecretKey = previousSecret
		http.DefaultClient = previousClient
	})
	common.TurnstileSecretKey = "test-secret"

	for _, test := range []struct {
		name          string
		enabled       bool
		token         string
		verification  string
		wantSucceeded bool
	}{
		{name: "disabled without token", wantSucceeded: true},
		{name: "enabled without token", enabled: true},
		{name: "enabled with invalid token", enabled: true, token: "invalid", verification: `{"success":false}`},
		{name: "enabled with valid token", enabled: true, token: "valid", verification: `{"success":true}`, wantSucceeded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			common.TurnstileCheckEnabled = test.enabled
			http.DefaultClient = &http.Client{Transport: turnstileRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				require.Equal(t, "test-secret", request.FormValue("secret"))
				require.Equal(t, test.token, request.FormValue("response"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(test.verification)),
					Header:     make(http.Header),
				}, nil
			})}

			router := gin.New()
			router.GET("/reset_password", TurnstileCheck(), func(context *gin.Context) {
				context.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/reset_password?turnstile="+test.token, nil)
			router.ServeHTTP(response, request)

			if test.wantSucceeded {
				assert.Equal(t, http.StatusNoContent, response.Code)
				return
			}
			assert.Equal(t, http.StatusOK, response.Code)
			var payload struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.False(t, payload.Success)
		})
	}
}
