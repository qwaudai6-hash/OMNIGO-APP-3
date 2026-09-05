package service

import (
	"context"
	"fmt"
	"log"

	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v3"
)

// RiderProximitySearch finds nearby riders using Redis geospatial queries.
// Uses GeoRadius with distance sorting for O(log N + M) performance.
type RiderProximitySearch struct {
	rdb redis.UniversalClient
}

func NewRiderProximitySearch(rdb redis.UniversalClient) *RiderProximitySearch {
	return &RiderProximitySearch{rdb: rdb}
}

// FindNearbyRiders finds riders within radiusKm of (pickupLng, pickupLat),
// sorted by distance (closest first), returning up to maxRiders.
func (rps *RiderProximitySearch) FindNearbyRiders(
	ctx context.Context,
	pickupLng, pickupLat float64,
	radiusKm float64,
	maxRiders int,
) ([]models.RiderDistance, error) {
	if rps.rdb == nil {
		return nil, nil
	}

	// Compute the H3 resolution-5 hex for the pickup location
	centerCoord := h3.GeoCoord{Latitude: pickupLat, Longitude: pickupLng}
	vendorHex := h3.FromGeo(centerCoord, 5)

	// Build list of geo keys to search (sharded by H3 hex)
	neighbors := h3.KRing(vendorHex, 1) // k=1 = ~15km radius
	geoKeys := make([]string, 0, len(neighbors))
	for _, hex := range neighbors {
		geoKeys = append(geoKeys, fmt.Sprintf("riders:locations:h3:%x", hex))
	}

	// Use GeoRadius with distance sorting (closest first)
	var allResults []models.RiderDistance
	seen := make(map[string]bool)

	for _, geoKey := range geoKeys {
		results, err := rps.rdb.GeoRadius(ctx, geoKey, pickupLng, pickupLat, &redis.GeoRadiusQuery{
			Radius:    radiusKm,
			Unit:      "km",
			WithCoord: true,
			WithDist:  true,
			Count:     maxRiders,
			Sort:      "ASC", // closest first
		}).Result()

		if err != nil {
			log.Printf("Warning: GeoRadius failed for %s: %v", geoKey, err)
			continue
		}

		for _, loc := range results {
			if seen[loc.Name] {
				continue
			}
			seen[loc.Name] = true
			allResults = append(allResults, models.RiderDistance{
				RiderTrackID: loc.Name,
				DistanceKm:   loc.Dist,
				Latitude:     loc.Latitude,
				Longitude:    loc.Longitude,
			})
		}
	}

	// Sort by distance (closest first) — merge across multiple geo keys
	sortRidersByDistance(allResults)

	// Cap to maxRiders
	if len(allResults) > maxRiders {
		allResults = allResults[:maxRiders]
	}

	return allResults, nil
}

// sortRidersByDistance performs insertion sort (fast for small N < 20).
func sortRidersByDistance(riders []models.RiderDistance) {
	for i := 1; i < len(riders); i++ {
		key := riders[i]
		j := i - 1
		for j >= 0 && riders[j].DistanceKm > key.DistanceKm {
			riders[j+1] = riders[j]
			j--
		}
		riders[j+1] = key
	}
}
