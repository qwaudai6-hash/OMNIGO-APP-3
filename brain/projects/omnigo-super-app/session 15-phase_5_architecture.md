# OMNIGO Phase 5 — Cart, Multi-Vendor Split Checkout & Gig Dispatch Architecture

This document maps out the system architecture and data flows for the E-Commerce Shopping Cart, Multi-Vendor Split Checkout, and real-time Delivery Gig Broadcast pipeline.

---

## 1. High-Level System Architecture

The following diagram illustrates how the frontend cart manager, local persistence, split-order dispatch pipelines, Go microservices, database, message queues, and Rust WebSocket gateway interact.

```mermaid
sequenceDiagram
    autonumber
    actor Customer as Customer Client
    participant Cart as CartProvider<br/>(Local Storage)
    participant OrderSvc as Go: order-service<br/>(Port 8088)
    participant DB as PostgreSQL DB<br/>(Port 5433)
    participant Kafka as Apache Kafka<br/>(Port 9092)
    participant DeliverySvc as Go: delivery-gig-service<br/>(Port 8084)
    participant Redis as Redis Cache<br/>(Port 6379)
    participant RustWS as Rust: WebSocket Gateway<br/>(Port 8081)
    actor Rider as Rider Client

    %% Cart Addition & Storage
    Customer->>Cart: Add items from different stores (Store A, Store B)
    Note over Cart: Items cached in-memory & persisted in SharedPreferences as JSON

    %% Checkout & Splitting
    Customer->>Cart: Tap "Checkout"
    Note over Cart: Group cart items by store_tracking_id
    
    %% Split order postings
    par Checkout Store A
        Cart->>OrderSvc: POST /orders (Store A items, total)
        OrderSvc->>DB: Lookup vendor_tracking_id from stores table
        OrderSvc->>DB: INSERT order (customer, store, product_tracking_ids)
        OrderSvc->>Kafka: Publish "orders.created" event (Order A)
        OrderSvc-->>Cart: HTTP 201 Created (ORD-A)
    and Checkout Store B
        Cart->>OrderSvc: POST /orders (Store B items, total)
        OrderSvc->>DB: Lookup vendor_tracking_id from stores table
        OrderSvc->>DB: INSERT order (customer, store, product_tracking_ids)
        OrderSvc->>Kafka: Publish "orders.created" event (Order B)
        OrderSvc-->>Cart: HTTP 201 Created (ORD-B)
    end

    Cart-->>Customer: Clear local Cart & display Success modal

    %% Async Gig Dispatch Loop
    Note over DeliverySvc: background listener consuming "orders.created"
    Kafka->>DeliverySvc: Consume "orders.created" (Order A & B)
    
    critical Delivery Gig Allocation
        DeliverySvc->>DB: INSERT delivery gig (GIG-xxxx, Status: 'broadcasting')
        DeliverySvc->>Redis: Execute H3 Hexagonal Ring Search (Riders at Resolution 8)
        Redis-->>DeliverySvc: Return nearby Rider tracking IDs (RIDR-xxxx)
        DeliverySvc->>Kafka: Publish "deliveries.broadcasted" (GIG-xxxx, Riders list)
    end

    %% WebSocket Broadcast
    Note over RustWS: background listener consuming "deliveries.broadcasted"
    Kafka->>RustWS: Consume "deliveries.broadcasted"
    RustWS->>Rider: Push real-time Gig Ping over secure WebSocket
    Rider-->>Customer: Display Gig marker on Leaflet Map
```

---

## 2. Core Components Specification

### A. Cart splitting (Flutter Client)
To satisfy the database model where an order is constrained to a single `store_tracking_id`, the client-side cart manager performs grouping logic before dispatching checkout transactions:
```dart
Map<String, List<CartItem>> itemsByStore = {};
for (var item in cartItems) {
  itemsByStore.putIfAbsent(item.storeTrackingId, () => []).add(item);
}
```
This isolates each shop's invoice, tax rate, and delivery pickup coordinates correctly.

### B. Database Schema & Tracking Chain Integrity
During the creation of an order record, the `order-service` executes a lookup query to find the parent owner of the merchant store:
```sql
SELECT vendor_tracking_id FROM stores WHERE store_tracking_id = $1;
```
By mapping the resolved `vendor_tracking_id` alongside the order's metadata, the system builds an immutable lineage chain:
- **`customer_tracking_id`**: Customer buyer identity.
- **`store_tracking_id`**: Specific storefront mapping.
- **`vendor_tracking_id`**: Merchant owner receiving payment payout.
- **`product_tracking_ids`**: Slice of purchased items.

This structure allows the **Admin Surveillance Engine** to track exact product provenance, commissions, and revenue flows.
