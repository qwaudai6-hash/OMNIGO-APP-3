package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fmt"
	"github.com/getsentry/sentry-go/gin"
	"github.com/omnigo/backend/internal/shared/telemetry"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/auth/handlers"
	"github.com/omnigo/backend/internal/auth/service"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/redis/go-redis/v9"
)

// buildEmailNotifier returns a function that POSTs to the email-service
// `/send` endpoint to deliver a transactional email. If the
// email-service URL isn't configured (e.g. dev mode), the notifier
// just logs the email payload so the developer can copy the link from
// the server logs.
//
// Production note: the email-service runs as a separate Node.js
// container with SMTP credentials mounted via Docker secrets. The
// auth-service talks to it over HTTP, never directly to SMTP.
func buildEmailNotifier(cfg *config.Config) func(ctx context.Context, to, subject, url string) {
	emailSvcURL := os.Getenv("EMAIL_SERVICE_URL")
	if emailSvcURL == "" {
		// Default to the docker-compose service name.
		emailSvcURL = "http://omnigo-email-service:8090"
	}
	return func(ctx context.Context, to, subject, url string) {
		// If email-service is configured, fire-and-forget POST. The
		// notifier runs in a goroutine already so blocking here is
		// fine.
		payload, _ := json.Marshal(map[string]interface{}{
			"to":      to,
			"subject": subject,
			"url":     url,
			"app":     "omnigo-auth",
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			emailSvcURL+"/send", bytes.NewReader(payload))
		if err != nil {
			log.Printf("email notifier: build request failed: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("email notifier: POST to %s failed: %v (falling back to log)", emailSvcURL, err)
			log.Printf("[EMAIL-DEV] to=%s subject=%q url=%s", to, subject, url)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			log.Printf("email notifier: %s returned %d", emailSvcURL, resp.StatusCode)
			return
		}
		log.Printf("email notifier: delivered to %s via %s", to, emailSvcURL)
	}
}

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9000)

	ctx := context.Background()

	// 2. Apply pending migrations, then initialize Database connection
	database.MigrateUpOrFail(ctx, cfg.DBWriterDSN)

	db, err := database.NewDB(ctx, cfg.DBWriterDSN, cfg.DBReaderDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// 3. Initialize Domain Layers with Dependency Injection
	svc := service.NewAuthService(db.Writer)

	// Redis client for ephemeral 2FA challenge storage + rate limit +
	// refresh-token reuse detection. We don't fail the boot if Redis
	// is missing in dev — the service falls back to in-memory storage.
	redisClient, err := cache.NewRedisClient(ctx, cfg.RedisAddrs)
	if err != nil {
		log.Printf("Warning: Failed to initialize redis (running with in-memory fallback): %v", err)
	} else {
		defer redisClient.Close()
		svc.WithRedis(redisClient.Client)
	}

	h := handlers.NewAuthHandler(svc)

	// Auth flow handler — forgot password, email verification, 2FA.
	// Wire to the email-service via HTTP — when not nil, the flow
	// handlers POST to email-service's /send endpoint and the
	// reset_token / verify_token are NOT returned in the API response
	// (production-safe).
	flowH := handlers.NewAuthFlowHandler(svc)
	flowH.WithNotifier(buildEmailNotifier(cfg))
	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
	}

	// 5. Setup Router
	router := gin.Default()
	router.RedirectTrailingSlash = false
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Security middleware: CORS + rate limiting (auth endpoints: 30 req/min stricter)
	router.Use(middleware.CORS())
	router.Use(middleware.RateLimit(rdb, 30, time.Minute))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "auth-service"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)
	flowH.RegisterRoutes(router, rdb)

	// 6. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.BindHost(), cfg.Port),
		Handler: router,
	}

	go func() {
		log.Printf("Starting Auth Service on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen and serve error: %v", err)
		}
	}()

	// Graceful shutdown listener
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down auth server...")

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Server exiting")
}
