# OMNIGO Session 12 — Frontend Validation Traps & Guard Tuning

This session focuses on resolving the three edge-case vulnerabilities in the Flutter frontend implementation.

---

## 1. Resolved Issues & Bug Fixes

### ❌ BUG #1: Future Type Mismatch in `main.dart`
- **Root Cause:** Using `.timeout(..., onTimeout: () { ... })` returning `void/null` inside an awaited `Future<void>` assignment caused a runtime Null operator crash during slow loads.
- **Fix:** Switched to a clean `try-catch` block wrapping standard `.timeout(Duration)`. If the timeout fires, a `TimeoutException` is thrown, caught safely by the outer catch block, logging a warning and letting the app initialize in guest/login mode without freezing.

### ❌ BUG #2: JSON API Boundary Mismatches
- **Root Cause:** Making sure that registration payload fields map perfectly between Flutter and Go models.
- **Fix:** Added default empty strings for optional parameters (region, cnic_url, license_url, business_name, address) inside the registration body on `dynamic_signup_screen.dart` to match the Go backend `RegisterRequest` structure exactly. This ensures that the JSON payloads never cause HTTP 400 Bad Request anomalies on either role (Vendor, Rider, Customer).

### ❌ BUG #3: Navigation Loop & Stack Bloat
- **Root Cause:** Redirection loop occurring if a mismatched session (e.g. a customer trying to access a vendor route) triggers auto-redirections continuously.
- **Fix:** Enhanced the `onGenerateRoute` guard. If a user is logged in but fails a specific role guard (e.g., customer trying to open vendor dashboard), we redirect them to their respective correct dashboard synchronously, rather than sending them back to the login screen (which would trigger the auto-login redirect loop). If they are completely logged out, we clear the session cache and show the signup/login screen.

---

## 2. Implementation Checklist
- `[x]` Refactor `lib/main.dart` to use the standard try-catch timeout block.
- `[x]` Update `lib/features/auth/presentation/screens/dynamic_signup_screen.dart` with role-aware auto-login forwarding and clean validation.
- `[x]` Update `lib/core/services/session_registry.dart` with session reset and validation helpers.

---

## 3. Verification Results
- Run `flutter analyze` inside `omnigo_app` (Completed successfully with 0 errors or warnings in the modified files!).
- Verified that an authenticated Customer hitting `/vendor-dashboard` gets redirected smoothly and synchronously to `/customer-dashboard` without any recursive login loop.
