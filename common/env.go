package common

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func GetEnvOrDefault(env string, defaultValue int) int {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	return os.Getenv(env)
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}

// GetSecretEnv reads a deployment secret from the process environment. The
// *_B64 form is used by the deployment workflow when a secret must cross an
// SSH boundary without appearing in a command argument or shell source.
func GetSecretEnv(env string) string {
	if value := strings.TrimSpace(os.Getenv(env)); value != "" {
		return value
	}
	encoded := strings.TrimSpace(os.Getenv(env + "_B64"))
	if encoded == "" {
		return ""
	}
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		SysError(fmt.Sprintf("failed to decode %s_B64", env))
		return ""
	}
	return strings.TrimSpace(string(value))
}
