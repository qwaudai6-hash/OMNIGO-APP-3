# Session 72 — Complete PayFast Integration Implementation Plan

> **Date:** September 7-8, 2026
> **Preceded by:** [[session_71_payfast_code_audit]] (Session 71 - Code Audit)
> **Scope:** Complete PayFast package - all payment methods, fully working, secure

---

## Goal

Implement **Complete PayFast Integration** with ALL payment methods:
- Card payments (existing - fix/verify)
- Hosted Checkout (wallet top-up) — **FIXED ✅**
- Raast P2M (instant bank transfer)
- JazzCash Wallet
- EasyPaisa Wallet
- IBFT Bank Transfer
- QR Payments
- Refund system (dashboard-based)

**Target:** Fully working, secure, production-ready

---

## Progress - Session 72

### Phase 0: Hosted Checkout Fix ✅ COMPLETED (Sept 8, 2026)

**Problem:** Hosted checkout form was returning HTTP 302 redirect to `/Ecommerce/Error/Index` instead of payment page.

**Root Cause:** Missing required form parameters. PayFast PHP sample code revealed 6 missing fields.

**Fixes Applied:**

1. **GetAccessToken API Fix** (`auth.go`):
   - Changed params from lowercase to UPPERCASE per PayFast spec
   - Added `BASKET_ID`, `TXNAMT`, `CURRENCY_CODE`, `APPLY_DISCOUNT` (required by PayFast)
   - Added `TokenContext` struct for basket details
   - Removed invalid `grant_type=client_credentials`

2. **Hosted Checkout Form Fix** (`payfast_charge.go`):
   - Added 6 missing form parameters: `TOKEN`, `MERCHANT_NAME`, `SIGNATURE`, `VERSION`, `TXNDESC`, `PROCCODE`, `TRAN_TYPE`, `ORDER_DATE`
   - Changed from GET redirect URL to POST auto-submit form (per PayFast PHP sample)
   - Added `fetchPayfastAccessToken()` helper function

3. **Parameter Names Fixed**:
   - All form params now UPPERCASE: `MERCHANT_ID`, `BASKET_ID`, `TXNAMT`, etc.
   - `SIGNATURE` = random string (NOT hash — per PayFast PHP sample)
   - `VERSION` = "MERCHANTCART-0.1"
   - `PROCCODE` = "00"
   - `TRAN_TYPE` = "ECOMM_PURCHASE"

**Verification:**
- ✅ Token API returns valid token with new params
- ✅ POST to PostTransaction returns HTTP 200 (was 302 before)
- ✅ PayFast returns auto-submit payment page HTML
- ✅ All 42+ unit/integration tests pass
- ✅ Go build clean

**Files Modified:**
- `internal/payment/payfast/auth.go` — TokenContext struct, UPPERCASE params
- `internal/payment/payfast/client.go` — GetAuthToken variadic params
- `internal/wallet/handler/payfast_charge.go` — Full form rewrite, fetchPayfastAccessToken()
- `internal/payment/payfast/payfast_integration_test.go` — Updated test params

**Live Test Result:**
```
POST https://ipguat.apps.net.pk/Ecommerce/api/Transaction/PostTransaction
HTTP 200 ✅
Set-Cookie: uat_payfast=yrptqdi5ed20xlleda5iux50 ✅
Response: Auto-submit form to ipguat2.apps.net.pk ✅
```

---

### Phase 1: JazzCash/EasyPaisa Wallet ✅ COMPLETED

**Backend Changes:**
- [x] Routes already registered in main.go
- [x] Added GET callback handler for WebView redirect (`CallbackRedirect`)
- [x] Handles success/cancel page rendering for WebView

**Frontend Changes:**
- [x] Enabled JazzCash/EasyPaisa (removed `isComingSoon: true`)
- [x] Added `walletInitiate()` endpoint in api_endpoints.dart
- [x] Added wallet payment handler in checkout_screen.dart
- [x] Added `_openWalletWebView()` helper function with WebView
- [x] Handles redirect URL, success/cancel detection

**Files Modified:**
- `backend/go-services/internal/payment_orchestrator/handlers/mobile_wallet_handler.go`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart`
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`

### Phase 1: Pending - Credentials Needed

**MISSING: JazzCash/EasyPaisa Credentials in .env**
```
JAZZCASH_MERCHANT_ID=     # NEEDS SANDBOX CREDENTIAL
JAZZCASH_PASSWORD=        # NEEDS SANDBOX CREDENTIAL
JAZZCASH_INTEGRITY_SALT= # NEEDS SANDBOX CREDENTIAL
EASYPAISA_STORE_ID=      # NEEDS SANDBOX CREDENTIAL
EASYPAISA_HASH_KEY=      # NEEDS SANDBOX CREDENTIAL
```

**Action Required:** Contact PayFast for sandbox credentials

---

## Current Status Analysis

### Payment Methods Status

| Method | Backend | Frontend | Status |
|--------|---------|----------|--------|
| **PayFast Card (Option C)** | ✅ Complete | ✅ Complete | Working |
| **PayFast Hosted Checkout (Wallet)** | ✅ Complete | ✅ Complete | **FIXED & VERIFIED ✅** |
| **JazzCash Wallet** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **EasyPaisa Wallet** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **Raast P2M** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **IBFT Transfer** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **QR Payments** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **Refund System** | ✅ Complete | N/A | Dashboard-based via PayFast |

### Research Findings

**PayFast Pakistan Supports:**
- ✅ Cards (Visa, MC, UnionPay)
- ✅ Raast P2M (May 2024 - First gateway to enable!)
- ✅ JazzCash Wallet
- ✅ EasyPaisa Wallet
- ✅ IBFT Bank Transfer
- ✅ QR Payments (static & dynamic)
- ✅ Alipay
- ✅ POS
- ❌ **Refund API: NOT AVAILABLE** - Dashboard-based only

**SDK Status:**
- `pay-pak` npm: PayFast = 🚧 (scaffold only)
- `pak-pay` Laravel: PayFast = 🚧 (scaffold only)
- Our Go implementation: MOST COMPLETE

---

## Implementation Phases

### Phase 1: JazzCash + EasyPaisa Wallet ✅ COMPLETED

**Tasks Completed:**
- [x] Enable JazzCash in Flutter (removed `isComingSoon: true`)
- [x] Enable EasyPaisa in Flutter (removed `isComingSoon: true`)
- [x] Added WebView for hosted checkout redirect
- [x] Added GET callback handler for WebView redirect
- [x] Added API endpoints in api_endpoints.dart

**Files Modified:**
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart`
- `backend/go-services/internal/payment_orchestrator/handlers/mobile_wallet_handler.go`

**Status:** ✅ COMPLETED - Needs credentials from PayFast

---

### Phase 2: Add Raast P2M (Bank Transfer) ✅ COMPLETED

**Tasks Completed:**
- [x] Created `raast.go` service implementing `PaymentGateway` interface
- [x] Created `raast_handler.go` with initiate + callback endpoints
- [x] Added Raast to orchestrator
- [x] Added Raast payment option in Flutter checkout
- [x] Added API endpoints in api_endpoints.dart
- [x] Go builds clean ✅
- [x] Flutter analyze clean ✅

**Files Created:**
- `backend/go-services/internal/payment/service/raast.go`
- `backend/go-services/internal/payment_orchestrator/handlers/raast_handler.go`

**Files Modified:**
- `backend/go-services/internal/payment/service/orchestrator.go`
- `backend/go-services/cmd/payment-orchestrator/main.go`
- `backend/go-services/.env.railway.example`
- `.env`
- `.env.example`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart`
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`

**Status:** ✅ COMPLETED - Needs credentials from PayFast

---

### Phase 3: Add IBFT Bank Transfer ✅ COMPLETED

**Tasks Completed:**
- [x] Created `ibft.go` service
- [x] Created `ibft_handler.go` with initiate + callback + CallbackRedirect
- [x] Added IBFT to orchestrator
- [x] Added IBFT payment option in Flutter checkout
- [x] Go builds clean ✅
- [x] Flutter analyze clean ✅

**Files Created:**
- `backend/go-services/internal/payment/service/ibft.go`
- `backend/go-services/internal/payment_orchestrator/handlers/ibft_handler.go`

**Files Modified:**
- `backend/go-services/internal/payment/service/orchestrator.go`
- `backend/go-services/cmd/payment-orchestrator/main.go`
- `.env`, `.env.example`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart`
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`

**Status:** ✅ COMPLETED - Needs credentials from PayFast

---

### Phase 4: Add QR Payments ✅ COMPLETED

**Tasks Completed:**
- [x] Created `qr.go` service
- [x] Created `qr_handler.go` with initiate + generate + callback endpoints
- [x] Added QR to orchestrator
- [x] Added QR payment option in Flutter checkout
- [x] Go builds clean ✅
- [x] Flutter analyze clean ✅

**Files Created:**
- `backend/go-services/internal/payment/service/qr.go`
- `backend/go-services/internal/payment_orchestrator/handlers/qr_handler.go`

**Files Modified:**
- `backend/go-services/internal/payment/service/orchestrator.go`
- `backend/go-services/cmd/payment-orchestrator/main.go`
- `.env`, `.env.example`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart`
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`

**Status:** ✅ COMPLETED - Needs credentials from PayFast

---

### Phase 5: Refund System (Dashboard-based) ✅ COMPLETED

**Tasks Completed:**
- [x] Modified refund handler to detect PayFast gateways (jazzcash, easypaisa, raast, ibft, qr, payfast)
- [x] Created refund_requests table model and repository
- [x] For PayFast payments: creates pending_manual refund request instead of calling gateway API
- [x] Returns pending_manual status to customer with message about manual processing
- [x] Go builds clean

**Files Created:**
- internal/payment/repository/refund_request_repository.go

**Files Modified:**
- internal/payment/handlers/refund_handler.go
- cmd/order-service/main.go

---

## Current Implementation Status

| Method | Backend | Frontend | Status |
|--------|---------|----------|--------|
| **PayFast Card (Option C)** | ✅ Complete | ✅ Complete | Working |
| **PayFast Hosted Checkout (Wallet)** | ✅ Complete | ✅ Complete | **FIXED & VERIFIED ✅** |
| **JazzCash Wallet** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **EasyPaisa Wallet** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **Raast P2M** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **IBFT Transfer** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **QR Payments** | ✅ Complete | ✅ Complete | **Needs Credentials** |
| **Refund API** | ❌ Missing | ❌ Missing | PayFast Has No Public API |

---

### Phase 3: Add QR Payments (PENDING)

**Tasks:**
- [ ] Create QR code generation endpoint
- [ ] Static QR for in-store
- [ ] Dynamic QR for online
- [ ] QR payment verification
- [ ] Flutter UI for QR display

**Files to Create:**
- `backend/go-services/internal/payment/service/qr.go`
- `backend/go-services/internal/payment_orchestrator/handlers/qr_handler.go`

---

### Phase 4: Refund System (Dashboard-based) (PENDING)

**Tasks:**
- [ ] Create refund request workflow
- [ ] Admin dashboard for manual refund approval
- [ ] Customer refund status view

---

## Completed Files

### Phase 1 - JazzCash + EasyPaisa
- Modified: `mobile_wallet_handler.go` - Added GET callback handler
- Modified: `checkout_screen.dart` - Enabled JazzCash/EasyPaisa options
- Modified: `api_endpoints.dart` - Added wallet endpoints

### Phase 2 - Raast P2M
- Created: `raast.go` service
- Created: `raast_handler.go`
- Modified: `orchestrator.go` - Added Raast gateway
- Modified: `main.go` - Registered routes
- Modified: `checkout_screen.dart` - Added Raast option + handler
- Modified: `api_endpoints.dart` - Added Raast endpoints

### Phase 3 - IBFT
- Created: `ibft.go` service
- Created: `ibft_handler.go`
- Modified: `orchestrator.go` - Added IBFT gateway
- Modified: `main.go` - Registered routes
- Modified: `checkout_screen.dart` - Added IBFT option + handler
- Modified: `api_endpoints.dart` - Added IBFT endpoints

### Phase 4 - QR Payments
- Created: `qr.go` service
- Created: `qr_handler.go`
- Modified: `orchestrator.go` - Added QR gateway + GenerateQR method
- Modified: `main.go` - Registered routes
- Modified: `checkout_screen.dart` - Added QR option + handler
- Modified: `api_endpoints.dart` - Added QR endpoints

Since PayFast has **NO public refund API**, we'll implement dashboard-based workflow:

**Backend Tasks:**
- [ ] Create refund request workflow
- [ ] Admin dashboard for manual refund approval
- [ ] Refund status tracking
- [ ] Notification to customer
- [ ] Ledger entry for refunds

**Frontend Tasks:**
- [ ] Customer refund request form
- [ ] Refund status view
- [ ] Admin refund approval UI

**Files to Create:**
- `backend/go-services/internal/payment/handlers/refund_handler.go` (enhance existing)
- `frontend/.../refund_request_screen.dart`
- `frontend/.../admin_refund_approval_screen.dart`

---

## Detailed Phase 1 Tasks

### Backend: Test JazzCash/EasyPaisa

**1. Verify Environment Variables**
```
JAZZCASH_MERCHANT_ID=
JAZZCASH_PASSWORD=
JAZZCASH_SALT=
JAZZCASH_API_URL=

EASYPAISA_STORE_ID=
EASYPAISA_HASH_KEY=
EASYPAISA_API_URL=
```

**2. Test jazzcash.go**
```go
// CreateCheckoutSession builds JazzCash form payload
// Returns: RedirectURL for WebView
// Fields: pp_Version, pp_TxnType, pp_MerchantID, etc.
// Signature: HMAC-SHA256 with integrity salt
```

**3. Test easypaisa.go**
```go
// CreateCheckoutSession returns EasyPaisa hosted URL
// Hash: AES-128-ECB encryption
// Redirect to: easypay.easypaisa.com.pk
```

**4. Verify mobile_wallet_handler.go**
```go
// POST /api/v1/payments/{gateway}/initiate
// Validates order ownership + amount
// Creates payment_transactions record
// Returns redirect URL
```

### Frontend: Enable JazzCash/EasyPaisa

**1. checkout_screen.dart**
```dart
// REMOVE: isComingSoon: true from jazzcash/easypaisa options
// ADD: Payment handler for wallet payments
// ADD: WebView for hosted checkout redirect
```

**2. payment_handler.dart**
```dart
// Future<Map<String, dynamic>> initiateWalletPayment(gateway, orderId)
// Opens WebView with redirect URL
// Listens for callback
// Returns payment result
```

**3. Handle callback**
```dart
// Parse: success/failure from URL params
// Show: OrderSuccessScreen or error
// Poll: for order status update
```

---

## API Endpoints Reference

### Current (JazzCash/EasyPaisa)

| Method | Endpoint | Handler | Auth | Status |
|--------|----------|---------|------|--------|
| POST | `/api/v1/payments/{gateway}/initiate` | `MobileWalletHandler.Initiate` | JWT | ✅ Implemented |
| GET | `/api/v1/payments/{gateway}/callback` | `MobileWalletHandler.Callback` | None | ✅ Implemented |

### Raast (Complete)

| Method | Endpoint | Handler | Auth | Status |
|--------|----------|---------|------|--------|
| POST | `/api/v1/payments/raast/initiate` | `RaastHandler.Initiate` | JWT | ✅ Implemented |
| GET | `/api/v1/payments/raast/callback` | `RaastHandler.Callback` | None | ✅ Implemented |

### IBFT (Complete)

| Method | Endpoint | Handler | Auth | Status |
|--------|----------|---------|------|--------|
| POST | `/api/v1/payments/ibft/initiate` | `IBFTHandler.Initiate` | JWT | ✅ Implemented |
| GET | `/api/v1/payments/ibft/callback` | `IBFTHandler.Callback` | None | ✅ Implemented |

### To Add (QR)

| Method | Endpoint | Handler | Auth | Status |
|--------|----------|---------|------|--------|
| POST | `/api/v1/payments/qr/generate` | `QRHandler.Generate` | JWT | ❌ Missing |
| GET | `/api/v1/payments/qr/status/{id}` | `QRHandler.Status` | JWT | ❌ Missing |

---

## Testing Plan

### Phase 1: Test JazzCash/EasyPaisa

**Sandbox Credentials Required:**
```env
JAZZCASH_MERCHANT_ID=MC090001
JAZZCASH_PASSWORD=
JAZZCASH_SALT=

EASYPAISA_STORE_ID=
EASYPAISA_HASH_KEY=
```

**Test Cases:**
1. Initiate JazzCash payment → get redirect URL
2. Open redirect URL in WebView
3. Complete payment on JazzCash form
4. Receive callback
5. Verify order status updated
6. Same flow for EasyPaisa

### Phase 2: Test Raast

**Requires:**
- PayFast Raast sandbox credentials
- API documentation

**Test Cases:**
1. Initiate Raast payment
2. Select bank
3. Authenticate with bank
4. Receive confirmation
5. Verify instant settlement

---

## Production Switch

**Current:** Sandbox/UAT
```env
JAZZCASH_API_URL=https://sandbox.jazzcash.com.pk/...
EASYPAISA_API_URL=https://sandbox.easypaisa.com.pk/...
PAYFAST_BASE_URL=https://ipguat.apps.net.pk/...
```

**Production:**
```env
JAZZCASH_API_URL=https://payments.jazzcash.com.pk/...
EASYPAISA_API_URL=https://easypay.easypaisa.com.pk/...
PAYFAST_BASE_URL=https://ipg1.apps.net.pk/...
```

**Switch Process:**
1. Get production credentials from PayFast
2. Update environment variables
3. Test with small amount
4. Monitor for 24 hours
5. Full production

---

## Files Modified in This Session

**Phase 1:**
- `frontend/omnigo_app/lib/features/customer/presentation/screens/checkout_screen.dart`
- `backend/go-services/internal/payment_orchestrator/handlers/mobile_wallet_handler.go`
- (others as needed)

---

## Next Steps

1. [Phase 4] Add QR Payments
2. [Phase 5] Add Refund System (Dashboard-based)

## Completed This Session

- [x] Phase 1: JazzCash + EasyPaisa Wallet
- [x] Phase 2: Raast P2M
- [x] Phase 3: IBFT Bank Transfer

---

## References

- [[session_71_payfast_code_audit]] - Previous audit findings
- [[session_33_execution_log]] - PayFast hardening session
- `backend/go-services/internal/payment/service/jazzcash.go` - JazzCash implementation
- `backend/go-services/internal/payment/service/easypaisa.go` - EasyPaisa implementation
- `backend/go-services/internal/payment_orchestrator/handlers/mobile_wallet_handler.go` - Mobile wallet handler
- PayFast Pakistan official: https://gopayfast.com
- **PayFast PHP Sample Code** (`payfast credianal files/re/Paymentuat.php`) — Reference for hosted checkout form params
- **PayFast Validation Hash** (`payfast credianal files/re/validation_hash_calculation.txt`) — IPN hash verification

---

## Email to PayFast (Draft)

**To:** [PayFast Contact Email]
**Subject:** OMNIGO App - Hosted Checkout Integration Query

Dear PayFast Team,

We are integrating PayFast payment gateway into the OMNIGO super app for wallet top-ups and order payments. We have successfully implemented the hosted checkout flow and verified it against your UAT sandbox (merchant ID: 102).

**Completed:**
- ✅ GetAccessToken API integration with basket details
- ✅ Hosted checkout form with all required parameters
- ✅ IPN callback validation using SHA256 hash
- ✅ Payment page rendering confirmed (HTTP 200)

**Queries:**
1. Are there any additional required parameters we may have missed?
2. For production deployment, what are the live API endpoints and credentials?
3. Do you provide sandbox credentials for JazzCash/EasyPaisa/Raast integration testing?
4. Is there a webhook/IPN retry mechanism we should implement?
5. What is the recommended approach for refund processing?

Please confirm if our integration meets your requirements or if any adjustments are needed.

Best Regards,
OMNIGO Development Team
