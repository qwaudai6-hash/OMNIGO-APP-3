import uuid
from fastapi import APIRouter
from pydantic import BaseModel
from ..ml_models import evaluate_fraud_graph

router = APIRouter()

class OrderData(BaseModel):
    user_id: str
    device_id: str
    amount: float
    ip_address: str
    payment_method: str

@router.post("/fraud/evaluate")
def evaluate_risk(order: OrderData):
    """
    Evaluates the risk of an order being part of a Fraud Ring using Heterogeneous Graph Neural Networks (HGNN).
    """
    
    # Evaluate device-sharing risk (the production heuristic — SP-PY-01)
    risk_score = evaluate_fraud_graph(order.user_id, order.device_id)

    is_risky = risk_score > 0.80

    # SP-PY-09: expose the REAL signal instead of a hardcoded None.
    from api.ml_models import fraud_device_sharing
    device_share_count = fraud_device_sharing.get(order.device_id, 0)

    return {
        "transaction_id": f"TXN-{uuid.uuid4().hex[:12].upper()}",
        "risk_score": round(risk_score, 4),
        "is_risky": is_risky,
        "action": "block_network" if is_risky else "approve",
        "model_version": "device_sharing_heuristic_v1",
        "device_share_count": device_share_count
    }
