# OMNIGO Session 26 — Plan (Option A)

> Recorded in-repo because Obsidian CLI is not installed in this environment.

## Goal
Implement OSRM dynamic routing + multi-vehicle pricing engine (surge / H3 density) for the rider/customer apps.

## Plan A — Add customer coords to `orders` table

1. **Database**: Add `customer_lat` / `customer_lng` columns to `orders` table in `infrastructure/postgres/init.sql`.
2. **Order request + model**: Add `dropoff_lat` / `dropoff_lng` to `CreateOrderRequest` and `Order`.
3. **Order event**: Add customer lat/lng to `models.OrderEvent` and emit in the Kafka `orders.created` payload.
4. **Order repository**: Persist `customer_lat` / `customer_lng` in `orders` on `CreateOrder`.
5. **Delivery gig enrichment**: When `delivery-service` consumes `orders.created`, lookup store pickup lat/lng from `stores` and use customer dropoff lat/lng from the event. Populate `deliveries.pickup_lat/lng` and `dropoff_lat/lng`.
6. **OSRM route endpoint**: `GET /api/v1/delivery/gig/:id/route` calls OSRM `/route/v1/driving/{pickup};{dropoff}`. Cache response in Redis under `route:delivery:<gig_id>` TTL 1h.
7. **Pricing endpoint**: `POST /api/v1/ride/estimate` computes fare for Bike / Rickshaw / Car using per-km matrices + H3 density surge multiplier.
8. **Flutter rider map**: consume route endpoint, draw polyline, show ETA.
9. **Validation**: `go build ./...`, `cargo check -p websocket-gateway`, `flutter analyze`, `run_all.sh` syntax check.

## Data-flow diagram

```text
Customer app
   |
   v
POST /api/v1/orders { ..., dropoff_lat, dropoff_lng }
   |
   v
order-service: Order table (customer_lat, customer_lng)
   |
   v
Kafka orders.created { order_id, vendor_store_tracking_id, user_tracking_id,
                       dropoff_lat, dropoff_lng, total_amount }
   |
   v
delivery-service
   |- lookup stores(latitude, longitude) by vendor_store_tracking_id
   |- create DeliveryGig with pickup_lat/lng + dropoff_lat/lng
   |
   v
GET /api/v1/delivery/gig/:id/route  -> OSRM /route/v1/driving
   ^                                  Redis cache route:delivery:<gig_id>
   |
Flutter rider map (polyline + ETA)
```

## Relevant files

- `backend/go-services/internal/order/models/order.go`
- `backend/go-services/internal/order/service/order_service.go`
- `backend/go-services/internal/order/repository/order_repository.go`
- `backend/go-services/internal/delivery/models/delivery.go`
- `backend/go-services/internal/delivery/service/delivery_service.go`
- `backend/go-services/internal/delivery/repository/delivery_repository.go`
- `backend/go-services/internal/delivery/handlers/delivery_handler.go`
- `backend/go-services/internal/delivery/router/delivery_router.go`
- `infrastructure/postgres/init.sql`
- `infrastructure/docker/docker-compose.yml`
- `frontend/omnigo_app/lib/features/rider/presentation/screens/rider_map_screen.dart`

## Notes

- OSRM container already in docker-compose (`osrm/osrm-backend:v5.27.1`).
- `deliveries` table already has `pickup_lat`, `pickup_lng`, `dropoff_lat`, `dropoff_lng`.
- H3 surge will reuse existing `current_h3_hexagon` + `h3-go` index lookups.
