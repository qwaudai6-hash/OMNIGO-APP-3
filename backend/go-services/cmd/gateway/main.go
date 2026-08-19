// Command gateway is the OMNIGO API Gateway — the single public entry point.
//
// Flutter talks to exactly one URL:
//
//	https://omnigo-app-production.up.railway.app
//
// This gateway is stateless and horizontally scalable. Run N replicas;
// Railway (or K8s) load-balances across them. All session state lives in
// Redis, never in process memory.
//
// Internal services are NOT exposed publicly. The gateway routes to them
// over Railway private networking using *_SERVICE_URL env vars.
//
// Usage:
//
//	go run ./cmd/gateway            # local dev (falls back to localhost ports)
//	SERVICE_NAME=gateway railway up # Railway production
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omnigo/backend/internal/gateway"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	gw := gateway.New(gateway.Options{
		Env: os.Getenv("APP_ENV"),
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown — drain in-flight requests on SIGTERM.
	go func() {
		log.Printf("OMNIGO API Gateway listening on :%s (stateless, horizontally scalable)", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gateway listen error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("gateway: received shutdown signal, draining...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("gateway: forced shutdown: %v", err)
	}
	log.Println("gateway: exited cleanly")
}
