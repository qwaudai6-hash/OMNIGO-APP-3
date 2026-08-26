"""OMNIGO AI Engine — FastAPI entry point.

Production-grade: trains all ML models on REAL PostgreSQL data at startup.
No synthetic/mock data.
"""
import logging
from contextlib import asynccontextmanager
from fastapi import FastAPI

from api.routes import (
    fraud_detection,
    eta_prediction,
    recommendation,
    surge_pricing,
    agentic_orchestrator,
)
from api.ml_models import train_all_models
from api.db import close_pool

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(name)s] %(levelname)s: %(message)s")
logger = logging.getLogger("ai-engine")


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Startup: train models in background, start auditor. Shutdown: close pool."""
    logger.info("=== OMNIGO AI Engine starting ===")

    # Health endpoint must respond immediately — train models in background
    import asyncio

    # SP-PY-06: surface background-training exceptions. A bare create_task()
    # used to swallow failures until GC — app started "healthy" with dead models.
    def _log_task_crash(task: asyncio.Task) -> None:
        if task.cancelled():
            return
        exc = task.exception()
        if exc is not None:
            logger.exception(f"[startup] background model training CRASHED: {exc}")

    training_task = asyncio.create_task(train_all_models())
    training_task.add_done_callback(_log_task_crash)

    from api.agents.financial_auditor import start_auditor
    logger.info("Starting Financial Auditor Daemon...")
    await start_auditor()

    yield

    logger.info("Shutting down AI Engine — closing DB pool...")
    await close_pool()


app = FastAPI(
    title="OMNIGO AI Engine",
    description="AI/ML Microservices for Fraud Detection, ETA, Recommendations, Surge Pricing — powered by real PostgreSQL data",
    version="2.0.0",
    lifespan=lifespan,
)

from api.routes import fraud_detection, eta_prediction, recommendation, surge_pricing, agentic_orchestrator
from api.agents import system_health_auditor

# Include API Routers
app.include_router(fraud_detection.router, prefix="/api/v1/ai", tags=["Fraud Detection"])
app.include_router(eta_prediction.router, prefix="/api/v1/ai", tags=["ETA Prediction"])
app.include_router(recommendation.router, prefix="/api/v1/ai", tags=["Recommendation Engine"])
app.include_router(surge_pricing.router, prefix="/api/v1/ai", tags=["Surge Pricing"])
app.include_router(agentic_orchestrator.router, prefix="/api/v1/ai", tags=["Agentic Orchestrator"])
app.include_router(system_health_auditor.router, prefix="/api/v1", tags=["System Health Auditor"])


@app.get("/health")
def health_check():
    return {"status": "ok", "service": "ai-engine", "version": "2.0.0", "data_source": "postgresql"}


@app.get("/api/v1/ai/audit/financial", tags=["Financial Auditor"])
async def get_financial_audit_status():
    from api.agents.financial_auditor import auditor
    result = await auditor.audit_ledger()
    return result


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=int(__import__("os").getenv("PORT", "8086")),
        log_level="info",
    )