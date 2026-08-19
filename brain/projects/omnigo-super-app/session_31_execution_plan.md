# Session 31 — Execution Plan: Admin Pagination Full UI

> **Created:** July 17, 2026
> **Preceded by:** [[OMNIGO_Project_Log]] (Session 41 addendum)
> **Architecture:** [[OMNIGO_SuperApp_Architecture_V2]]

---

## 📋 Goal

Wire the existing paginated admin backend into a complete Flutter admin pagination UI with page navigation, page-size selector, and a role filter — replacing the page-1-only, `total`-ignoring fetch behavior in `admin_surveillance_screen.dart`.

---

## 📐 Architecture / Grounding

Backend already fully paginated; all three lists return `{items, total, limit, offset}`:
- `GET /api/admin/users?role&limit&offset` → `ListAllUsers` (`backend/go-services/internal/admin/service.go:239`, supports `role` filter)
- `GET /api/admin/users/pending?limit&offset` → `ListPendingVerifications` (`backend/go-services/cmd/admin-service/main.go:120`)
- `GET /api/admin/verifications/pending?limit&offset` → `verificationService.ListPendingVerifications` (`backend/go-services/cmd/admin-service/main.go:162`)
- `parsePagination` (main.go:23): limit 1–100 (default 20), offset ≥ 0.

**No backend change required.** `OMNIGO_Project_Log.md` runs to Session 41; individual `session_*` notes stop at 28. No pagination/admin note existed — this work is new, not a duplicate.

---

## ⚡ Execution Steps

### 1. State additions (single file)
- Users: `_usersLimit=20, _usersOffset=0, _usersTotal=0, _userRole=''`
- Pending: `_pendingLimit=20, _pendingOffset=0, _pendingTotal=0`
- Verifications: `_verifLimit=20, _verifOffset=0, _verifTotal=0`

### 2. Fetch methods — append params + capture `total`
- `_fetchAllUsers()`: `Uri.parse('$_adminBase/users?role=$_userRole&limit=$_usersLimit&offset=$_usersOffset')`; `data['total']` → `_usersTotal`.
- `_fetchPendingVerifications()`: `?limit=$_pendingLimit&offset=$_pendingOffset` → `_pendingTotal`.
- `_fetchVerifications()`: `?limit=$_verifLimit&offset=$_verifOffset` → `_verifTotal`.
- Refresh + role change reset offset → 0 before refetch (via `resetOffset: true` arg).

### 3. Shared widget `_buildPagination(total, limit, offset, {onPrev, onNext, onLimit})`
- `‹ Prev` disabled at offset 0 · `Page (offset~/limit)+1 / (total/limit).ceil()` · `Next ›` disabled at last page · limit `DropdownButton` {20,50,100} snapping offset to page boundary (via `_set*Limit` helpers).

### 4. Wire into tabs
- Pending + KYC/KYB: pagination row above list.
- Users: role `DropdownButton` (All / Customer / Vendor / Rider) + pagination; role change resets offset & refetches.

### 5. Verify
- `flutter analyze lib/features/admin/presentation/screens/admin_surveillance_screen.dart` → **No issues found.**

---

## Skipped (YAGNI)
- Free-text user search (no backend endpoint).
- Infinite scroll (page buttons suffice for admin).
- Cross-tab persisted page state (per-tab offset vars suffice).
- Pagination on the Lineage tab (single-record lookup, not a list).
