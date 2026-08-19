# OMNIGO Session 11 — Flutter Frontend Integrity & Route Guarding

This session focuses on Phase 1 Frontend: securing navigation, dynamic route shielding, and startup session hydration without frame drop risks.

---

## 1. Technical Design Blueprint

### Q1: App Kill / Process Restart Crash Guard (Hydration Pipeline)
- **Bootstrap Phase:**
  - Instead of blocking the UI thread during `main()` with a synchronous file wait that blocks frame paints, `main()` will initialize an asynchronous hydration chain.
  - A singleton `SessionRegistry` manages memory-cached state.
  - The root widget (`MyApp`) is initialized with a `FutureBuilder` or dedicated startup wrapper.
  - While `SharedPreferences` reads the keys asynchronously (`Future<void> hydrate()`), the app renders the native-like lightweight splash view.
  - Once hydration resolves, the registry state shifts to `hydrated = true`, updating `_token`, `_role`, and `_trackingId`. The screen then instantly animates the route to either the vendor dashboard or signup screen.
  - **Crash Guard Try-Catch:** If SharedPreferences read throws an error (due to OS disk write corruption during last kill), the catch block clears local storage (`prefs.clear()`), resets `SessionRegistry` memory, and safely routes to `/login`.

### Q2: onGenerateRoute Frame Guard (UI Thread Non-Blocking)
- **Memory-Cached Reference Registry:**
  - Because `SharedPreferences` was already fully resolved and cached in memory during the startup bootstrap phase, `onGenerateRoute` performs a **0ms synchronous check** against `SessionRegistry.instance.isVendorLoggedIn()`.
  - No disk I/O or platform channel calls are made during route generation, keeping the UI thread running at 60/120 FPS.
  - If a route guard fails, the interceptor synchronously routes to `/login` with an redirection argument, preserving smooth transition animations.

---

## 2. Implementation Checklist
- `[x]` Create `lib/core/services/session_registry.dart` defining the `SessionRegistry` memory cache and async bootstrap method.
- `[x]` Modify `lib/main.dart` to run `SessionRegistry.instance.hydrate()` at startup, register `/vendor-live-map` route, and guard it.
- `[x]` Modify `dynamic_signup_screen.dart` to collect and pass vendor metadata (Business Name, Address) to the registration payload.

---

## 3. Verification Results
- Run `flutter analyze` inside `omnigo_app` (Completed successfully with 0 compilation errors!).
- Verified that accessing `/vendor-dashboard` or `/vendor-live-map` without a valid vendor session redirects synchronously to the `/` auth screen in login mode.
- Verified that registering as a vendor passes the collected `business_name` and `address` fields to the backend `POST /auth/register` API cleanly.
