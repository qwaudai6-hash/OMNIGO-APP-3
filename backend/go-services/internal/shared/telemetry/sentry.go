package telemetry

import (
	"log"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry initializes the Sentry SDK if a DSN is provided.
// Returns a flush function to be deferred in main.
func InitSentry(dsn string, env string) func() {
	if dsn == "" || dsn == "YOUR_DSN_HERE" || dsn == "YOUR_SENTRY_DSN_HERE" {
		log.Println("Sentry is disabled (no valid DSN provided)")
		return func() {}
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		TracesSampleRate: 1.0, // Adjust in production
	})
	if err != nil {
		log.Printf("Sentry initialization failed: %v\n", err)
		return func() {}
	}

	log.Println("Sentry successfully initialized")
	return func() {
		sentry.Flush(2 * time.Second)
	}
}
