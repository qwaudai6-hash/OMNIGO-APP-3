package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/auth/handlers"
	"github.com/omnigo/backend/internal/auth/service"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
)

func main() {
	// 1. Load Configuration
	cfg, err := config.LoadConfig(".")
	if err != nil {
		log.Printf("Warning: Failed to load config file, relying on ENV vars: %v", err)
	}

	// For local testing without config, provide defaults if empty
	if cfg == nil {
		cfg = &config.Config{
			Port:        8080, // Auth Service on Gateway Routing Ingress 8080
			DBWriterDSN: "postgres://admin:admin123@localhost:5432/omnigo?sslmode=disable",
			DBReaderDSN: "postgres://admin:admin123@localhost:5432/omnigo?sslmode=disable",
		}
	} else if cfg.Port == 0 {
		cfg.Port = 8080
	}

	ctx := context.Background()

	// 2. Initialize Database connection
	db, err := database.NewDB(ctx, cfg.DBWriterDSN, cfg.DBReaderDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// 3. Initialize Domain Layers
	svc := service.NewAuthService(db.Writer)
	h := handlers.NewAuthHandler(svc)

	// 4. Setup Router
	router := gin.Default()

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "auth-service"})
	})

	h.RegisterRoutes(router)

	// 5. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    ":8080",
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
