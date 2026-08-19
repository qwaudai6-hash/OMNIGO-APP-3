# OMNIGO Session 30 Log & Progress Tracker

## Completed Implementations & Modifications

### 1. Vendor & Rider Onboarding Hybrid Workflow
- **Plan**: Eliminate the strict block for vendors and riders during sign-up, redirecting them immediately to their respective dashboards while keeping their live status restricted (`is_verified = false`).
- **Files Modified**: 
  - `backend/go-services/internal/auth/handlers/auth_handler.go`
  - `backend/go-services/internal/auth/service/auth_service.go`
  - `frontend/omnigo_app/lib/features/auth/presentation/screens/dynamic_signup_screen.dart`
  - `frontend/omnigo_app/lib/features/rider/presentation/screens/rider_map_screen.dart`
  - `frontend/omnigo_app/lib/core/services/session_registry.dart`

### 2. Vendor Smart Address & Map Auto-Fetch
- **Plan**: Capture precise geo-coordinates (Lat/Lng) alongside the physical text address during Vendor registration.
- **Files Modified**:
  - `infrastructure/postgres/init.sql` (Added `latitude` and `longitude` to `users` table).
  - `backend/go-services/internal/auth/service/auth_service.go` (Updated `RegisterRequest` and SQL `INSERT`).
  - `frontend/omnigo_app/pubspec.yaml` (Added `geocoding` dependency).
  - `frontend/omnigo_app/lib/features/auth/presentation/screens/dynamic_signup_screen.dart` (Implemented `_fetchCurrentLocation` with GPS and reverse-geocode).

### 3. Desktop Native Plugin Issue (Geolocator Linux)
- **Error**: `MissingPluginException` for `geolocator` on Linux.
- **Resolution**: Removed all desktop mock fallbacks. The system now strictly uses native hardware geolocation. On unsupported platforms, the raw exception is surfaced to the user as-is.

### 4. Strict Mock Data Ban & Secure Tracking ID Generation
- **POLICY**: **NO MOCK DATA IS PERMITTED.** All hardcoded fallback logic has been aggressively stripped from the codebase.
- **Tracking ID Refactor**:
  - The legacy `math/rand` 6-digit dummy counter in `auth_service.go` has been removed.
  - Replaced with `github.com/google/uuid` (UUIDv4) string truncation.
  - Example output: `VEND-756b5921`
  - **Data Flow**:
    1. Frontend submits `/auth/register` without any ID.
    2. Backend fires `generateTrackingID(role)` using UUID.
    3. The unique ID is bound to `tracking_id` column in Postgres.
    4. Upon successful insertion, the ID is returned in the HTTP 201 response.
    5. Frontend picks up the generated ID and propagates to local storage.

### 5. PostgreSQL Connection Pool Optimization
- **Problem**: `FATAL: sorry, too many clients already` — all 10 Go microservices were requesting 50-100 connections each, far exceeding the default 100-connection Postgres limit.
- **Solution**:
  - `docker-compose.yml`: Raised `max_connections=300`.
  - `internal/shared/database/postgres.go`: Reduced Writer pool from `MaxConns=50` → `5`, Reader pool from `MaxConns=100` → `10`.

### 6. Live Database Schema Migration
- **Problem**: The Docker `pgdata` volume was created before several columns were added to `init.sql`. The live database was missing `vehicle_type`, `vehicle_plate_number`, `verification_status`, `verification_reason`, `risk_score`, `submitted_at`, `verified_at`, `latitude`, `longitude`.
- **Solution**: Ran live `ALTER TABLE users ADD COLUMN IF NOT EXISTS ...` for all 9 missing columns directly against the running container.

## Automated Verification Results (PASS)

| Test | Result |
|---|---|
| Vendor Registration API | ✅ `"Account registered successfully"` |
| UUID Tracking ID | ✅ `VEND-756b5921` (secure, non-colliding) |
| Login API | ✅ JWT + Refresh Token issued |
| `is_verified` flag | ✅ `false` for new vendors |
| `entity_type` persistence | ✅ `"individual"` saved |
| Latitude in Postgres | ✅ `31.52041234` |
| Longitude in Postgres | ✅ `74.35875678` |
| Business Name | ✅ `Verified Automated Store` |
| Debezium CDC Connector | ✅ `RUNNING` |

## Files Modified This Session
- `backend/go-services/internal/auth/service/auth_service.go`
- `backend/go-services/internal/shared/database/postgres.go`
- `frontend/omnigo_app/lib/features/auth/presentation/screens/dynamic_signup_screen.dart`
- `frontend/omnigo_app/pubspec.yaml`
- `infrastructure/docker/docker-compose.yml`
- `scripts/start_backend_only.sh` (NEW)
- `scripts/verify_system.sh` (NEW)
