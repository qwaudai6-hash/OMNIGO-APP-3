"""PostgreSQL connection pool for the AI Engine.

Reads DATABASE_URL (same as Go services) and exposes a shared asyncpg pool.
All AI models fetch real data from here — no synthetic/mock data anywhere.
"""
import os
import asyncpg
import logging

logger = logging.getLogger("ai-engine.db")

_pool: asyncpg.Pool | None = None


async def init_pool() -> asyncpg.Pool:
    """Initialize the global connection pool. Call once at startup."""
    global _pool
    if _pool is not None:
        return _pool

    dsn = os.getenv("DATABASE_URL")
    if not dsn:
        raise RuntimeError("DATABASE_URL environment variable is required")

    # asyncpg expects postgres:// or postgresql:// scheme. Railway provides
    # postgres:// but some older URLs use postgresql:// — normalize.
    if dsn.startswith("postgresql://"):
        dsn = dsn.replace("postgresql://", "postgres://", 1)

    _pool = await asyncpg.create_pool(
        dsn=dsn,
        min_size=int(os.getenv("DB_MIN_CONNS", "2")),
        max_size=int(os.getenv("DB_MAX_CONNS", "10")),
        command_timeout=30,
        ssl=os.getenv("DB_SSL", "require") == "require" and "require" or None,
    )
    logger.info("PostgreSQL pool initialized (real data mode)")
    return _pool


async def get_pool() -> asyncpg.Pool:
    """Returns the initialized pool. Panics if init_pool wasn't called."""
    if _pool is None:
        return await init_pool()
    return _pool


async def close_pool():
    """Graceful shutdown — close all connections."""
    global _pool
    if _pool is not None:
        await _pool.close()
        _pool = None
        logger.info("PostgreSQL pool closed")