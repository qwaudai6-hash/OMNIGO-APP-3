# OMNIGO Session 14 — Phase 5 Bug Audit & Resolution Logs

This log documents the identification and resolution of two critical runtime bugs in the Phase 5 cart and checkout implementations.

---

## 1. Go Database Scanner Null Pointer Crash

### Bug Identification
- **Symptom:** Retrieving orders (via single fetch, customer history, or vendor lists) returned HTTP 500 or database scan errors.
- **Root Cause:** 
  The active PostgreSQL container schema has all order columns except `id` configured as nullable. The fields `rider_tracking_id`, `delivery_type`, `payment_gateway`, `otp_code`, `created_at`, and `updated_at` are inserted as NULL during creation.
  In Go `order_repository.go`, scanning these columns directly into non-pointer variables (`string` and `time.Time`) threw standard Go DB driver type mismatch errors when encountering `NULL` driver values.
- **Impact:** Complete failure of the customer order history screen and merchant dashboard.
- **Resolution:**
  1. Updated the insert query to explicitly generate timestamps using `NOW()` parameters:
     ```sql
     INSERT INTO orders (..., created_at, updated_at) VALUES (..., NOW(), NOW())
     ```
  2. Refactored all scanner queries to read nullable attributes into pointers (`*string` and `*time.Time`) and conditionally dereference them:
     ```go
     var deliveryType *string
     // Scan into &deliveryType
     if deliveryType != nil {
         order.DeliveryType = *deliveryType
     }
     ```

---

## 2. Flutter Cart Zero-Dollar Price Parsing Bug

### Bug Identification
- **Symptom:** Adding items from the main catalog grid card resulted in a total cart price of `$0.00`.
- **Root Cause:** 
  The catalog grid loads products via the `product-service` API which represents prices under the key `price`. The `ProductDetailsScreen` loads them from vendor schemas using `base_price`.
  The initial `CartProvider.addItem()` only searched for `base_price` and `price_usd`, leaving the parsed double as `0.0`.
- **Impact:** Fraudulent checkout requests with zero-dollar totals sent to the payment/order gateways.
- **Resolution:**
  - Standardized the price resolving hierarchy in the state manager to inspect `price` as the primary key:
    ```dart
    final price = (product['price'] ?? product['base_price'] ?? product['price_usd'] ?? 0.0).toDouble();
    ```
