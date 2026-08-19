package h3search

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v3"
)

func FindNearbyRiders(ctx context.Context, rdb redis.UniversalClient, lat, lng float64, radiusKm float64, maxResults int) []string {
	centerCoord := h3.GeoCoord{Latitude: lat, Longitude: lng}
	vendorHex := h3.FromGeo(centerCoord, 5)
	neighbors := h3.KRing(vendorHex, 1)

	var riders []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, hex := range neighbors {
		wg.Add(1)
		go func(h h3.H3Index) {
			defer wg.Done()
			key := fmt.Sprintf("riders:locations:h3:%x", h)
			res, err := rdb.GeoRadius(ctx, key, lng, lat, &redis.GeoRadiusQuery{
				Radius: radiusKm,
				Unit:   "km",
			}).Result()
			if err == nil && len(res) > 0 {
				mu.Lock()
				for _, loc := range res {
					riders = append(riders, loc.Name)
				}
				mu.Unlock()
			}
		}(hex)
	}
	wg.Wait()

	if len(riders) == 0 {
		log.Printf("H3 Search: No riders found near (%.4f, %.4f) within %.1fkm", lat, lng, radiusKm)
		return nil
	}
	if len(riders) > maxResults {
		return riders[:maxResults]
	}
	return riders
}
