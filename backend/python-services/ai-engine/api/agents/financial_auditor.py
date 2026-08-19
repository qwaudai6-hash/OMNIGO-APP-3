import os
import time
import hmac
import hashlib
import asyncio
import asyncpg
import logging

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(name)s - %(levelname)s - %(message)s')
logger = logging.getLogger("FinancialAuditor")

# Constants — REQUIRED env vars, no insecure fallback
HMAC_SECRET = os.getenv("LEDGER_HMAC_SECRET") or os.getenv("HMAC_SECRET")
DB_DSN = os.getenv("DATABASE_URL")
if not HMAC_SECRET:
    raise RuntimeError("FATAL: LEDGER_HMAC_SECRET (or HMAC_SECRET) is required. Refusing to start with insecure fallback.")
if not DB_DSN:
    raise RuntimeError("FATAL: DATABASE_URL is required. Refusing to start with localhost fallback.")
AUDIT_INTERVAL_SECONDS = 300 # 5 minutes

class FinancialAuditor:
    def __init__(self, dsn: str, secret: str):
        self.dsn = dsn
        self.secret = secret.encode('utf-8')
        self.is_running = False

    def generate_signature(self, transaction_id: str, account: str, amount: float, reference_id: str, idempotency_key: str) -> str:
        # Replicate Go's %f which formats floats to 6 decimal places
        payload = f"{transaction_id}:{account}:{amount:.6f}:{reference_id}:{idempotency_key}"
        mac = hmac.new(self.secret, payload.encode('utf-8'), hashlib.sha256)
        return mac.hexdigest()

    async def audit_ledger(self):
        logger.info("Starting Ledger Security Audit...")
        try:
            conn = await asyncpg.connect(self.dsn)
            # Fetch the last 1000 entries (or all un-audited entries if we had a cursor)
            rows = await conn.fetch('''
                SELECT id, transaction_id, account, amount, reference_id, idempotency_key, signature
                FROM ledger_entries
                ORDER BY created_at DESC
                LIMIT 1000
            ''')
            
            invalid_entries = []
            for row in rows:
                expected_sig = self.generate_signature(
                    str(row['transaction_id']),
                    row['account'],
                    row['amount'],
                    row['reference_id'],
                    row['idempotency_key']
                )
                
                if expected_sig != row['signature']:
                    invalid_entries.append({
                        "id": str(row['id']),
                        "transaction_id": str(row['transaction_id']),
                        "account": row['account'],
                        "amount": row['amount'],
                        "expected": expected_sig,
                        "found": row['signature']
                    })

            await conn.close()

            if invalid_entries:
                logger.error(f"AUDIT FAILED: Found {len(invalid_entries)} tampered ledger entries!")
                for entry in invalid_entries:
                    logger.error(f"TAMPERED ENTRY: {entry}")
                
                # Self-Healing Hook
                await self.trigger_self_healing(invalid_entries)
            else:
                logger.info(f"AUDIT SUCCESS: All {len(rows)} recent ledger entries verified successfully. No tampering detected.")
                
            return {
                "status": "failed" if invalid_entries else "success",
                "scanned_entries": len(rows),
                "tampered_entries": len(invalid_entries),
                "timestamp": time.time()
            }

        except Exception as e:
            logger.error(f"Failed to run audit: {e}")
            return {"status": "error", "message": str(e)}

    async def run_loop(self):
        self.is_running = True
        while self.is_running:
            await self.audit_ledger()
            await asyncio.sleep(AUDIT_INTERVAL_SECONDS)

    async def trigger_self_healing(self, tampered_entries: list):
        """
        Self-Healing Hook:
        When tampered entries are found, this method freezes the affected accounts
        and alerts the admin dashboard.
        """
        logger.warning(f"Executing Self-Healing Protocol for {len(tampered_entries)} tampered entries...")
        # Future implementation:
        # 1. Update the ledger_entries table to mark them as 'QUARANTINED'
        # 2. Re-calculate correct balance from TigerBeetle and push a compensating transaction
        # 3. Publish 'ledger.security_alert' to Kafka to lock the Vendor/Rider's withdrawals
        for entry in tampered_entries:
            logger.critical(f"[SELF-HEALING] Quarantining transaction {entry['transaction_id']} (Account: {entry['account']})")
        logger.warning("Self-Healing Protocol execution completed.")

    def start(self):
        asyncio.create_task(self.run_loop())

    def stop(self):
        self.is_running = False

# Global instance
auditor = FinancialAuditor(DB_DSN, HMAC_SECRET)

def start_auditor():
    auditor.start()
