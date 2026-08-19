from fastapi import APIRouter
from pydantic import BaseModel
import datetime
import math
from ..ml_models import predict_eta

router = APIRouter()

class RouteData(BaseModel):
    pickup_lat: float
    pickup_lng: float
    dropoff_lat: float
    dropoff_lng: float
    vehicle_type: str # car, bike, truck

def haversine(lat1, lon1, lat2, lon2):
    R = 6371.0 # Earth radius in kilometers
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    a = math.sin(dlat / 2)**2 + math.cos(math.radians(lat1)) * math.cos(math.radians(lat2)) * math.sin(dlon / 2)**2
    c = 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))
    distance = R * c
    return distance

@router.post("/eta/predict")
def get_eta(route: RouteData):
    """
    Predicts the Estimated Time of Arrival (ETA) for a rider/delivery using a Spatio-Temporal Deep Ensemble (LightGBM + MLP).
    """
    # 1. Calculate haversine distance
    distance_km = haversine(route.pickup_lat, route.pickup_lng, route.dropoff_lat, route.dropoff_lng)
    
    # 2. Encode vehicle type (0=bike, 1=rickshaw/truck, 2=car)
    v_type = 2
    if route.vehicle_type == "bike":
        v_type = 0
    elif route.vehicle_type == "truck" or route.vehicle_type == "rickshaw":
        v_type = 1
        
    current_hour = datetime.datetime.now().hour
    
    # 3. Predict ETA
    predicted_minutes = predict_eta(distance_km, v_type, current_hour)
    
    return {
        "eta_minutes": round(predicted_minutes, 1),
        "distance_km": round(distance_km, 2),
        "confidence": 0.90 if distance_km > 1 else 0.95,
        "model_version": "deep_ensemble_lgbm_mlp"
    }
