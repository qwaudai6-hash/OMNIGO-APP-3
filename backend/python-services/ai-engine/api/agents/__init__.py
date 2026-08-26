"""OMNIGO AI Engine Autonomous Multi-Agent Package.

Imports are guarded so a missing/broken optional agent module never breaks
engine startup (main.py imports this package at boot).
"""
from .financial_auditor import FinancialAuditor
from .concierge_agent import OmnigoConciergeAgent

try:  # optional agent — market_maker.py not present in all deployments
    from .market_maker import MarketMakerAgent
except ImportError:  # pragma: no cover
    MarketMakerAgent = None

__all__ = ["FinancialAuditor", "OmnigoConciergeAgent", "MarketMakerAgent"]
