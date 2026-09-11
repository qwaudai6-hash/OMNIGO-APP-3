# COD Settlement Redesign: JazzCash/EasyPaisa → PayFast Card Payment

## Current System Analysis

### COD Flow (End-to-End)

1. Customer places COD order → `orders.payment_gateway = 'cod'`
2. Delivery gig created → `deliveries.is_cod = true`
3. Rider delivers → `cash_in_hand_paisa += orderTotal`
4. `cod_debts` row created, `cash_receivable → rider_cod_debt` ledger entry
5. Rider taps "Pay COD" → JazzCash/EasyPaisa deep-link generated
6. Rider pays via wallet app → webhook to `/api/v1/payments/cod/settlement`
7. Settlement: `rider_cod_debt → central_escrow → vendor_wallet`

### Current Problems

1. JazzCash/EasyPaisa dependency - rider must have wallet app
2. No card payment option for COD settlement
3. Rider tracking ID not tracked in settlement records
4. Webhook reliability issues

---

## Proposed Solution: PayFast Card Payment

### New Flow

1. Rider taps "Pay COD" → navigates to card payment screen
2. Rider enters card details (number, expiry, CVV)
3. PayFast temporary token requested with `cod_debt_id` as basket ID
4. 3DS verification if required (WebView)
5. Tokenized transaction initiated
6. On success: ledger entries created, vendor escrow released
7. `cod_debts.status = 'settled'`

---

## Implementation Plan

### Phase 1: Backend Changes

#### 1.1 Database Migration
File: `backend/go-services/migrations/0050_cod_debts_rider_tracking_id.up.sql`

```sql
ALTER TABLE cod_debts 
ADD COLUMN rider_tracking_id VARCHAR(100);

CREATE INDEX idx_cod_debts_rider_tracking_id 
ON cod_debts(rider_tracking_id);
```

#### 1.2 New Endpoint: `POST /api/v1/payments/cod/card-payment`
File: `backend/go-services/internal/payment_orchestrator/handlers/cod_handler.go`

- Accepts: `card_number`, `expiry_month`, `expiry_year`, `cvv`, `order_tracking_id`
- Extracts rider_tracking_id from JWT
- Calls `PayFastService.ProcessCODCardPayment()`
- Returns: 3DS challenge URL or settlement confirmation

#### 1.3 New Method: `PayFastService.ProcessCODCardPayment()`
File: `backend/go-services/internal/payment_orchestrator/service/payfast_service.go`

- Similar to `ProcessPayment()` but for COD settlement
- Uses `cod_debt_id` as basket ID
- Stores `rider_tracking_id` in payment metadata
- On 3DS success, calls `CODHandler.SettleCODDebt()`

#### 1.4 Modify: `CODHandler.SettleCODDebt()`
File: `backend/go-services/internal/payment_orchestrator/handlers/cod_handler.go`

- Remove JazzCash/EasyPaisa webhook handler
- Support PayFast card payment settlement
- Add `rider_tracking_id` to settlement records

#### 1.5 Update: `RecordCODDebt()`
File: `backend/go-services/internal/delivery/repository/delivery_repository.go`

- Accept `rider_tracking_id` parameter
- Store in `cod_debts` table

### Phase 2: Frontend Changes

#### 2.1 New Screen: `CODCardPaymentScreen`
File: `lib/features/payment/presentation/screens/cod_card_payment_screen.dart`

- Card number input with formatting
- Expiry date picker (MM/YY)
- CVV input (masked)
- Amount display (from `cod_debts.amount_owed`)
- "Pay Now" button
- 3DS WebView for verification

#### 2.2 Modify: `CODSettlementScreen`
File: `lib/features/payment/presentation/screens/cod_settlement_screen.dart`

- Remove JazzCash/EasyPaisa option
- Add "Pay with Card" button
- Navigate to `CODCardPaymentScreen`

### Phase 3: Testing

#### 3.1 Unit Tests
- Test `ProcessCODCardPayment` with mock PayFast
- Test settlement ledger entries
- Test error handling

#### 3.2 Integration Tests
- Test full flow: rider pays → ledger entries → vendor escrow
- Test 3DS callback handling

#### 3.3 E2E Tests
- Test on Railway staging environment
- Test with real PayFast sandbox credentials

---

## Technical Details

### PayFast Card Payment Flow

```
CODHandler.CardPayment()
  → PayFastService.ProcessCODCardPayment()
    → payfast.Client.GetTemporaryTransactionToken()
    → if 3DS required: return 3DS URL
    → payfast.Client.InitiateTokenizedTransaction()
    → CODHandler.SettleCODDebt()
```

### Ledger Entries

```
# Settlement
Debit: rider_cod_debt (amount_owed)
Credit: central_escrow (amount_owed)

# Vendor Release
Debit: central_escrow (vendor_escrow_amount)
Credit: vendor_wallet (vendor_escrow_amount)
```

### Database Schema Changes

```sql
-- Migration: Add rider_tracking_id to cod_debts
ALTER TABLE cod_debts 
ADD COLUMN rider_tracking_id VARCHAR(100);

CREATE INDEX idx_cod_debts_rider_tracking_id 
ON cod_debts(rider_tracking_id);
```

---

## Risk Assessment

### Low Risk
- Reusing existing PayFast card payment flow (already tested for customer checkout)
- Ledger entries follow same pattern as JazzCash/EasyPaisa settlement

### Medium Risk
- 3DS verification timing (rider may abandon during 3DS)
- IPN webhook delays

### Mitigation
- Add timeout handling for 3DS verification
- Add reconciliation worker for pending settlements

---

## Timeline

1. **Phase 1 (Backend)**: 2-3 days
   - Database migration
   - New endpoint
   - Modify settlement logic

2. **Phase 2 (Frontend)**: 2-3 days
   - New payment screen
   - Modify settlement screen

3. **Phase 3 (Testing)**: 1-2 days
   - Unit tests
   - Integration tests
   - E2E tests

**Total**: 5-8 days

---

## Success Criteria

1. Rider can pay COD debt via debit/credit card
2. 3DS verification works correctly
3. Ledger entries are accurate
4. Vendor receives payment within expected timeframe
5. No JazzCash/EasyPaisa dependency remains

---

## Appendix: File Locations

- `backend/go-services/internal/payment_orchestrator/handlers/cod_handler.go`: COD handler
- `backend/go-services/internal/payment_orchestrator/service/payfast_service.go`: PayFast service
- `backend/go-services/internal/payment/payfast/api.go`: PayFast API client
- `backend/go-services/internal/wallet/service/rider_wallet_service.go`: Rider wallet
- `backend/go-services/internal/delivery/repository/delivery_repository.go`: COD debt recording
- `backend/go-services/internal/escrow/service.go`: Vendor escrow
- `backend/go-services/internal/payment_orchestrator/commission.go`: COD split calculation
