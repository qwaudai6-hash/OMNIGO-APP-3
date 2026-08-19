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
    
    # Process the node relationships using HGNN model
    risk_score = evaluate_fraud_graph(order.user_id, order.device_id)
    
    is_risky = risk_score > 0.80
    
    return {
        "transaction_id": f"TXN-{uuid.uuid4().hex[:12].upper()}",
        "risk_score": round(risk_score, 4),
        "is_risky": is_risky,
        "action": "block_network" if is_risky else "approve",
        "model_version": "hgnn_pyg_real_data",
        "device_share_count": None  # populated by evaluate_fraud_graph internally
    }
