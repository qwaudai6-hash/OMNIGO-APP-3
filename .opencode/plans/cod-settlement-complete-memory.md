# OMNIGO COD Settlement System - Complete Memory

## Project Overview
- **Platform**: OMNIGO Super App (Go microservices + Flutter frontend)
- **Backend**: `/home/arise/Downloads/OMNIGO-APP-2-main/backend/go-services`
- **Railway URL**: `https://omnigo-app-3-production.up.railway.app`
- **Admin**: `admin@omnigo.pk` / `Omn!go@YSeoWRg5UwYB` (role: admin)
- **Test Vendor**: `VEND-28bdd793`, **Test Store**: `STOR-9c02385a`
- **Services**: 15 Go services compiled with `CGO_ENABLED=1`, monolith spawns children at `/app/bin/<name>`
- **auth-service**: DOWN on Railway (port 9000) - all other 9 services healthy

---

## Current COD Flow (End-to-End)

### 1. Order Creation (`order_service.go`)
```
Customer selects COD → orders.payment_gateway = 'cod'
Order created with vendor_store_tracking_id
Kafka event: orders.created
```

### 2. Delivery Gig Creation (`delivery_service.go:166-299`)
```
HandleNewOrder() receives Kafka event
Creates gig with IsCOD: true, OrderTotal: order.TotalAmount
OTP code generated for customer verification
Broadcasts gig to nearby riders via Redis Geospatial
```

### 3. Rider Accepts Gig (`delivery_service.go:308-364`)
```
AcceptGig() → AcceptGigWithEligibility() in delivery_repository.go
Checks rider suspension status
Publishes Kafka: deliveries.accepted
```

### 4. Rider Delivers (`delivery_service.go:366-519`)
```
UpdateGigStatus(status='completed')
Verifies OTP code matches
Credits rider wallet (skip ledger for COD - central_escrow not funded yet)
AddCODCollection() → cash_in_hand_paisa += orderTotal
RecordCODDebt() → cod_debts row created
CreateCODDebtLedger() → cash_receivable → rider_cod_debt ledger entry
```

### 5. Rider Settles COD (`cod_handler.go:162-285`)
```
POST /api/v1/payments/cod/pay-now
Gateway: jazzcash | easypaisa | payfast

JazzCash/EasyPaisa:
  → Generates deep-link URL
  → Rider pays via wallet app
  → Webhook to /api/v1/payments/cod/settlement

PayFast:
  → Generates hosted checkout redirect URL
  → Rider pays via PayFast hosted page
  → IPN callback to /api/v1/payments/cod/settlement
```

### 6. Settlement (`cod_handler.go:301-470`)
```
POST /api/v1/payments/cod/settlement

1. Verify idempotency on webhook_event_id
2. Read COD debt from cod_debts table
3. Calculate COD split (higher admin cut + COD surcharge)
4. Build ledger transfers with idempotency keys:
   - rider_cod_debt → cash_receivable (clear debt)
   - cash_receivable → admin_revenue (commission)
   - cash_receivable → vendor_locked_escrow (vendor portion)
   - cash_receivable → central_escrow (delivery fee)
5. Execute ledger multi-transfer
6. Update cod_debts.status = 'settled'
7. Decrement rider cash_in_hand_paisa
8. Create escrow hold for vendor portion
```

### 7. Vendor Gets Paid (`escrow/service.go`)
```
CreateHold() → vendor_locked_escrow
processHoldTx() → releases to vendor_wallet when order status = 'delivered' or 'completed'
```

---

## PayFast Card Payment Flow (Customer Checkout)

### 1. Initiate Payment (`payfast_service.go:308-986`)
```
ProcessPayment()
  → Validate input (card number, expiry, CVV)
  → Fraud checks (velocity, anomaly)
  → Lock order row (FOR UPDATE)
  → Calculate split (admin + vendor + delivery)
  → Insert payment_transactions (status: 'pending')
  → apps.net.pk flow: Generate auto-submitting HTML form
  → OR Token flow: GetTemporaryTransactionToken()
```

### 2. 3DS Verification (if required)
```
Token response has Data3DSHTML
  → Save instrument_token, gateway_txn_id, 3ds_secureid in metadata
  → Update payment_transactions status = '3ds_required'
  → Return ThreeDSHtml to frontend (WebView)
  → User completes 3DS challenge
  → POST /api/v1/payments/payfast/3ds_callback
  → Handle3DSCallback() resumes with InitiateTokenizedTransaction()
```

### 3. Direct Tokenized Capture (if no 3DS)
```
InitiateTokenizedTransaction()
  → If success: VerifyAndSettle()
  → If 3DS required: return 3DS HTML
```

### 4. Verify & Settle (`payfast_service.go:1098-1156`)
```
VerifyAndSettle()
  → GetTransactionStatus() from PayFast
  → Verify amount matches
  → ExecuteSplit() with outbox event
```

### 5. Execute Split (`payfast_service.go:1158-1280`)
```
ExecuteSplit()
  → Lock order (FOR UPDATE)
  → Calculate split
  → Update payment_transactions status = 'settlement_pending'
  → Update orders with split amounts
  → Create outbox_event for settlement worker
  → Ledger: payfast_holding → admin_revenue, vendor_locked_escrow, central_escrow
```

---

## COD Split Calculation (`commission.go:108-162`)
```
CalculateCODSplit()
  → Read store commission_rate (default 2%)
  → Read delivery_fee from deliveries table
  → COD surcharge: 0.5% additional
  → adminRevenue = orderTotal × (commissionRate + codSurcharge) / 100
  → deliveryEscrow = deliveryFee
  → vendorEscrow = orderTotal - adminRevenue - deliveryEscrow
```

---

## Database Schema

### cod_debts Table
```sql
CREATE TABLE cod_debts (
  id UUID PRIMARY KEY,
  order_tracking_id VARCHAR(100) NOT NULL,
  rider_tracking_id VARCHAR(100) NOT NULL,
  amount_owed BIGINT NOT NULL,  -- paisa
  status VARCHAR(20) DEFAULT 'pending',  -- pending, settled, failed
  settled_via VARCHAR(50),  -- jazzcash, easypaisa, payfast
  settled_at TIMESTAMP,
  webhook_event_id VARCHAR(255),
  created_at TIMESTAMP DEFAULT NOW()
);
```

### payment_transactions Table
```sql
CREATE TABLE payment_transactions (
  transaction_id VARCHAR(100) PRIMARY KEY,
  order_tracking_id VARCHAR(100) NOT NULL,
  gateway VARCHAR(50),
  amount DECIMAL(10,2),
  currency VARCHAR(10),
  status VARCHAR(30),  -- pending, processing, 3ds_required, settlement_pending, settled, failed
  kind VARCHAR(30),  -- payment, refund
  idempotency_key VARCHAR(255),
  gateway_txn_id VARCHAR(100),
  metadata JSONB,
  callback_processed_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT NOW(),
  updated_at TIMESTAMP DEFAULT NOW()
);
```

### rider_wallet Table
```sql
CREATE TABLE rider_wallet (
  rider_tracking_id VARCHAR(100) PRIMARY KEY,
  balance_paisa BIGINT DEFAULT 0,
  lifetime_earnings_paisa BIGINT DEFAULT 0,
  cash_in_hand_paisa BIGINT DEFAULT 0,
  is_cash_blocked BOOLEAN DEFAULT FALSE,
  updated_at TIMESTAMP DEFAULT NOW()
);
```

### orders Table (relevant columns)
```sql
CREATE TABLE orders (
  order_tracking_id VARCHAR(100) PRIMARY KEY,
  total_amount_paisa BIGINT,
  payment_gateway VARCHAR(50),
  payment_status VARCHAR(30),
  status VARCHAR(30),
  store_tracking_id VARCHAR(100),
  vendor_tracking_id VARCHAR(100),
  customer_tracking_id VARCHAR(100),
  admin_commission_paisa BIGINT,
  vendor_escrow_paisa BIGINT,
  delivery_escrow_paisa BIGINT,
  ...
);
```

---

## Ledger Accounts
```
AccountCentralEscrow     = "central_escrow"
AccountPayFastHolding    = "payfast_holding"
AccountAdminRevenue      = "admin_revenue"
AccountVendorLockedEscrow = "vendor_locked_escrow"
AccountRiderWallet       = "rider_wallet"
AccountRiderCODDebt      = "rider_cod_debt"
AccountCashReceivable    = "cash_receivable"
AccountGatewayClearing   = "gateway_clearing"
```

---

## Key Files Reference

| File | Purpose |
|------|---------|
| `cod_handler.go` | COD confirm, pay-now, settlement, list-debts |
| `payfast_service.go` | PayFast card payment flow, 3DS, IPN, settle |
| `payfast_handler.go` | PayFast HTTP handlers |
| `payfast/api.go` | PayFast API client (token, transaction, status) |
| `payfast/client.go` | PayFast client config, HMAC verification |
| `payfast/models.go` | PayFast request/response models |
| `rider_wallet_service.go` | AddCODCollection, CreateCODDebtLedger, DecrementCODCollection |
| `delivery_service.go` | COD delivery completion, ledger entries |
| `delivery_repository.go` | RecordCODDebt, AcceptGigWithEligibility |
| `escrow/service.go` | CreateHold, processHoldTx |
| `commission.go` | CalculateCODSplit, CalculateSplit |
| `order_service.go` | CancelCODDebtsForOrder, triggerRefund |
| `settlement_worker.go` | cleanupStalePending (3DS fix) |
| `refund_outbox_worker.go` | RefundProcessorWorker |
| `refund_handler.go` | COD reversal logic |

---

## Financial Audit Fixes Applied

1. **Ghost Refund** → `RefundProcessorWorker` directly credits customer wallet
2. **Stale 3DS** → `settlement_worker.go` queries PayFast before marking failed
3. **Partial Refund** → `order_items` gets `fulfillment_status`, `refund_status`, `refund_amount_paisa`
4. **COD Amount Trust** → `cod_handler.go` validates against `order.total_amount_paisa`
5. **Rider Cash Block** → `delivery_repository.go` uses `RIDER_CASH_BLOCK_THRESHOLD`
6. **Escrow Release Guard** → `escrow/service.go` checks `orders.status IN ('delivered','completed')`

---

## Proposed Change: JazzCash/EasyPaisa → PayFast Card

### What Changes
1. **Remove**: JazzCash/EasyPaisa deep-link generation in `PayNow()`
2. **Add**: New endpoint `POST /api/v1/payments/cod/card-payment`
3. **Add**: `rider_tracking_id` column to `cod_debts` table
4. **Add**: New method `PayFastService.ProcessCODCardPayment()`
5. **Modify**: `CODHandler.Settlement()` to support PayFast card settlement

### New Flow
```
Rider taps "Pay COD"
  → POST /api/v1/payments/cod/card-payment
  → {card_number, expiry_month, expiry_year, cvv, order_tracking_id}
  → PayFastService.ProcessCODCardPayment()
    → GetTemporaryTransactionToken() with cod_debt_id as basket_id
    → If 3DS required: return 3DS URL
    → InitiateTokenizedTransaction()
    → On success: settle COD debt
  → SettleCODDebt()
    → Verify amount matches
    → Ledger: rider_cod_debt → central_escrow
    → CreateHold for vendor
    → Update cod_debts.status = 'settled'
    → Decrement cash_in_hand_paisa
```

### Database Migration
```sql
-- 0050_cod_debts_rider_tracking_id.up.sql
ALTER TABLE cod_debts ADD COLUMN rider_tracking_id VARCHAR(100);
CREATE INDEX idx_cod_debts_rider_tracking_id ON cod_debts(rider_tracking_id);
```

---

## Environment Variables (PayFast)
```
PAYFAST_MERCHANT_ID
PAYFAST_SECURED_KEY
PAYFAST_HASH_KEY (optional, defaults to SECURED_KEY)
PAYFAST_MERCHANT_NAME
PAYFAST_BASE_URL (or PAYFAST_API_URL)
PAYFAST_SUCCESS_URL
PAYFAST_FAILURE_URL
PAYFAST_WEB_ORIGIN
PAYFAST_3DS_CALLBACK_URL
PAYFAST_CHECKOUT_URL
PAYFAST_GATEWAY_TIMEOUT_SECONDS (default: 20)
PAYFAST_MERCHANT_CATEGORY (default: 0001)
INTERNAL_CALLBACK_SECRET (or HMAC_SECRET)
PUBLIC_BASE_URL
DEFAULT_CURRENCY (default: PKR)
```

---

## Implementation Status (Session: 2026-09-11)

### Completed:

1. **Database Migration** (`0050_cod_debts_rider_tracking_id.up.sql`)
   - Added `rider_tracking_id` column to `cod_debts` table
   - Created index for fast lookups

2. **Backend: PayFastService.ProcessCODCardPayment()**
   - New method in `payfast_service.go`
   - Handles card payment flow: validate → token → 3DS → settle
   - Uses `cod_debt_id` as basket ID for PayFast
   - Includes `settleCODDebt()` for ledger entries and vendor escrow

3. **Backend: CODHandler.CardPayment()**
   - New handler in `cod_handler.go`
   - Route: `POST /api/v1/payments/cod/card-payment`
   - Requires JWT auth (rider or admin role)
   - Injected PayFast client via constructor

4. **Frontend: RiderCODCardPaymentScreen**
   - New Flutter screen at `rider_cod_card_payment_screen.dart`
   - Card input with formatting
   - 3DS WebView for bank verification
   - Success/failure handling

5. **Frontend: Rider Wallet Screen Updated**
   - Removed JazzCash/EasyPaisa options
   - Added "Pay with Card" button
   - Navigates to new card payment screen

6. **API Endpoints Updated**
   - Added `codCardPayment()` to `api_endpoints.dart`

### Build Status:
- All 15 Go services built successfully
- Flutter analysis: only warnings/info, no errors

### Next Steps:
1. Deploy to Railway (auto-runs migration)
2. Test with real PayFast sandbox credentials
3. Monitor settlement flow
