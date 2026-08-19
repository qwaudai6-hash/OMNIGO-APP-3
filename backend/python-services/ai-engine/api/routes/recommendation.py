from fastapi import APIRouter
from pydantic import BaseModel
from typing import List, Optional

router = APIRouter()

class RecommendRequest(BaseModel):
    user_tracking_id: str
    recently_viewed_products: List[int] # Sequence of numeric item IDs

class CoBoughtRequest(BaseModel):
    product_tracking_id: str
    top_k: Optional[int] = 4

@router.post("/recommendations")
def get_user_recommendations(req: RecommendRequest):
    """
    Returns personalized product recommendations using the SASRec Transformer model.
    It predicts the NEXT item the user will click based on their chronological sequence.
    """
    from ..ml_models import get_next_item_recommendations
    recommended_products = get_next_item_recommendations(req.recently_viewed_products, top_k=3)

    return {
        "user_tracking_id": req.user_tracking_id,
        "recommendations": recommended_products,
        "algorithm": "sasrec_transformer_attention"
    }

@router.post("/frequently-bought-together")
async def get_frequently_bought_together(req: CoBoughtRequest):
    """
    Returns item tracking IDs frequently co-purchased with the target product
    using real SQL co-occurrence query on order_items table.
    """
    from ..ml_models import get_co_bought_recommendations
    recommendations = await get_co_bought_recommendations(req.product_tracking_id, req.top_k)
    return {
        "product_tracking_id": req.product_tracking_id,
        "recommendations": recommendations,
        "algorithm": "sql_co_occurrence_collaborative_filtering"
    }

