package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/map/handlers"
	"github.com/omnigo/backend/internal/map/service"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/health"
)

func main() {
	cfg := config.LoadConfig(".")
	cfg.Port = config.EnvPort(9010)

	mapSvc := service.NewMapService(cfg.MapLibreAPIKey, cfg.MapLibreStyleURL)
	osrmURL := os.Getenv("OSRM_URL")
	if osrmURL == "" {
		osrmURL = os.Getenv("OSRM_BASE_URL")
	}
	photonURL := os.Getenv("PHOTON_URL")
	mapSvc.SetEndpoints(osrmURL, photonURL)

	log.Printf("OMNIGO Map Service initialized. Style: %s, OSRM: %s, Photon: %s", cfg.MapLibreStyleURL, osrmURL, photonURL)

	h := handlers.NewMapHandler(mapSvc)

	router := gin.Default()
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "map-service"})
	})
	router.GET("/readyz", health.Ready())

	h.RegisterRoutes(router)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	go func() {
		log.Printf("Starting OMNIGO Map Service on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Map service listen error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Map service forced shutdown: %v", err)
	}
	log.Println("Map service exited")
}
