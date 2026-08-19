# OMNIGO Session 14 — Customer Cart, Order Dispatch Backend Integration, & Unified Platform Execution

This session details the architectural design, database DDL synchronizations, and code changes for Phase 5: E-Commerce Shopping Cart & E2E Order Dispatch.

---

## 1. Technical Design Blueprint

### Q1: E-Commerce Shopping Cart & Multi-Vendor Split Checkout
- **Constraint:** Cart must persist across restarts. Since orders in the database are bound to a single vendor store (`store_tracking_id`), checking out a cart containing products from different stores must not violate relational constraints.
- **Design:**
  - **Offline Persistent Cache:**
    - Create a `CartItem` model mapping `productId`, `name`, `price`, `quantity`, `storeTrackingId`, and `imageUrl`.
    - Persist the cart list as a JSON string under the key `customer_cart` in `SharedPreferences`.
  - **Frontend Multi-Vendor Split Checkout:**
    - In the checkout flow, cart items are grouped dynamically by `store_tracking_id`.
    - The frontend dispatches separate parallel HTTP POST requests to the `/api/v1/orders` endpoint for each group.
    - Each order is assigned its own unique tracking ID (`ORD-xxxx`), ensuring correct processing by the respective vendor store.

### Q2: Database Schema Inconsistencies & Table Harmonization
- **Constraint:** The Postgres container maps host port `5433` but skips DDL schema initialization on restart. `deliveries` and `rides` tables were missing entirely. The Go `order-service` SQL queries contained outdated column references.
- **Design:**
  - **DDL Execution:** Created the missing `deliveries` and `rides` tables in the database matching Go model fields.
  - **Go Order Models & Queries Realignment:**
    - Mapped Go models to Postgres columns: `order_tracking_id`, `customer_tracking_id`, `store_tracking_id`, `product_tracking_ids` (array).
    - Updated query parameters in `order_repository.go` to use native slice bindings for `product_tracking_ids`.

### Q3: Advanced Order Querying & State Mutation APIs
- **Constraint:** The Go `order-service` only supported retrieving a single order. Vendors must be able to pull pending orders, and customers must see order histories. Vendors also need to accept orders.
- **Design:**
  - **New Fetch Routes:**
    - `GET /api/v1/orders/customer/:customer_id` ➔ Returns order history for a customer.
    - `GET /api/v1/orders/vendor/:vendor_id` ➔ Returns orders for stores owned by a vendor.
  - **New Mutation Route:**
    - `PATCH /api/v1/orders/:tracking_id/status` ➔ Modifies status (e.g. `accepted`, `shipped`, `delivered`), triggering Kafka events.

---

## 2. Production Code Changes

### A. Go Backend Repository Updates (`order_repository.go`)
[order_repository.go](file:///home/phatan/Documents/OMNIGO E COMMERCE APP/backend/go-services/internal/order/repository/order_repository.go)
```go
func (r *OrderRepository) CreateOrder(ctx context.Context, order *models.Order) error {
	// Lookup vendor_tracking_id from stores table to maintain trace integrity
	if order.VendorTrackID == "" {
		storeQuery := `SELECT vendor_tracking_id FROM stores WHERE store_tracking_id = $1`
		_ = r.reader.QueryRow(ctx, storeQuery, order.VendorStoreTrackID).Scan(&order.VendorTrackID)
	}

	query := `
		INSERT INTO orders (order_tracking_id, customer_tracking_id, store_tracking_id, vendor_tracking_id, product_tracking_ids, status, total_amount, currency)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		order.TrackingID,
		order.UserTrackID,
		order.VendorStoreTrackID,
		order.VendorTrackID,
		order.ProductTrackingIDs,
		order.TotalAmount,
		order.Currency,
	).Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt)

	return err
}

func (r *OrderRepository) UpdateOrderStatus(ctx context.Context, trackingID string, status string) error {
	query := `
		UPDATE orders
		SET status = $1, updated_at = NOW()
		WHERE order_tracking_id = $2
	`
	res, err := r.writer.Exec(ctx, query, status, trackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("order not found")
	}
	return nil
}
```

### B. Flutter Cart State Management (`cart_provider.dart`)
[cart_provider.dart](file:///home/phatan/Documents/OMNIGO E COMMERCE APP/frontend/omnigo_app/lib/core/services/cart_provider.dart)
```dart
class CartProvider extends ChangeNotifier {
  final Map<String, CartItem> _items = {};

  Map<String, CartItem> get items => {..._items};

  int get itemCount => _items.values.fold(0, (sum, item) => sum + item.quantity);
  double get totalAmount => _items.values.fold(0.0, (sum, item) => sum + (item.price * item.quantity));

  Future<void> loadCart() async {
    final prefs = await SharedPreferences.getInstance();
    final data = prefs.getString('customer_cart') ?? '[]';
    final List<dynamic> list = jsonDecode(data);
    _items.clear();
    for (var json in list) {
      final item = CartItem.fromJson(json);
      _items[item.productId] = item;
    }
    notifyListeners();
  }

  Future<void> addItem(ProductModel product) async {
    if (_items.containsKey(product.productTrackingId)) {
      _items[product.productTrackingId]!.quantity++;
    } else {
      _items[product.productTrackingId] = CartItem(
        productId: product.productTrackingId,
        name: product.name,
        price: product.price,
        quantity: 1,
        storeTrackingId: product.storeTrackingId,
      );
    }
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> removeItem(String productId) async {
    _items.remove(productId);
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> clearCart() async {
    _items.clear();
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> _saveToStorage() async {
    final prefs = await SharedPreferences.getInstance();
    final list = _items.values.map((item) => item.toJson()).toList();
    await prefs.setString('customer_cart', jsonEncode(list));
  }
}
```

---

## 3. Implementation Checklist
- `[ ]` Update database DSN ports to `5433` in `order-service`, `delivery-gig-service`, and `ride-service`.
- `[ ]` Update SQL queries and Go schemas in `order-service` matching active Postgres column names.
- `[ ]` Add order list APIs for customers and vendors to Go `order-service`.
- `[ ]` Add status patch API for order acceptances.
- `[ ]` Implement local persistent `CartProvider` in Flutter.
- `[ ]` Update frontend `ApiClient` and `ApiEndpoints` to support orders endpoints.
- `[ ]` Build Cart Checkout UI, supporting multi-vendor split checkouts.
- `[ ]` Wire Vendor Dashboard UI to load real orders from Go backend.
- `[ ]` Update `start_omnigo.sh` to run the complete suite of services.
