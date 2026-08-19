package security

import (
	"fmt"
	"os"
	"strings"
)

// MustEnv reads an environment variable and panics if it is empty or
// whitespace-only. Use this for any secret, key, or DSN that the service
// cannot operate without. It replaces every "getEnv() with development
// fallback" anti-pattern in the codebase.
//
// The error message intentionally includes the variable name and a
// remediation hint so operators don't have to dig through code to figure
// out what to set.
func MustEnv(key string) string {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		panic(fmt.Sprintf(
			"FATAL: %s environment variable is not set. "+
				"Refusing to start with an insecure fallback. "+
				"Add %s to your .env file, Kubernetes Secret, or container env. "+
				"Generate secrets with: openssl rand -base64 64",
			key, key,
		))
	}
	return v
}

// MustEnvAny returns the first non-empty value among the supplied keys.
// Panics if none of them are set. Useful when the same secret was historically
// expected under multiple names (e.g. JWT_SECRET vs JWT_SECRET_KEY) and we
// want to migrate without a hard cutover.
func MustEnvAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	panic(fmt.Sprintf(
		"FATAL: none of [%s] are set. "+
			"Set at least one of these environment variables before starting the service.",
		strings.Join(keys, ", "),
	))
}

// OptionalEnv returns the value of the env var, or the supplied default if
// it is empty. ONLY use this for non-secret configuration (timeouts, ports,
// feature flags). Never use this for secrets — use MustEnv instead.
func OptionalEnv(key, defaultValue string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return defaultValue
}
