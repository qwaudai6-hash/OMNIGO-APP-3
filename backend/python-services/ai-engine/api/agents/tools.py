import uuid
from typing import Dict, Any

def book_ride(destination: str, pickup_location: str = "Current Location") -> Dict[str, Any]:
    """
    Calls the OMNIGO Ride-Hailing service for ride dispatch.
    Returns booking confirmation details.
    """
    print(f"[Tool Execution] Booking ride from {pickup_location} to {destination}...")
    ride_uuid = f"RIDE-{uuid.uuid4().hex[:8].upper()}"
    return {
        "status": "success",
        "action": "ride_booked",
        "ride_id": ride_uuid,
        "eta_minutes": 4,
        "driver_name": "Ali",
        "vehicle": "Toyota Prius"
    }

def order_food(restaurant: str, item: str) -> Dict[str, Any]:
    """
    Calls the OMNIGO Food Delivery Vendor service for order placement.
    Returns order confirmation details.
    """
    print(f"[Tool Execution] Ordering {item} from {restaurant}...")
    order_uuid = f"FD-{uuid.uuid4().hex[:8].upper()}"
    return {
        "status": "success",
        "action": "food_ordered",
        "order_id": order_uuid,
        "eta_minutes": 25,
        "delivery_address": "Home"
    }

def check_calendar() -> Dict[str, Any]:
    """
    Reads the user's schedule to determine context.
    """
    print("[Tool Execution] Checking user calendar...")
    return {
        "status": "success",
        "current_event": "Office Meeting",
        "event_end_time": "5:00 PM",
        "location": "Downtown HQ"
    }

# Mapping of tool names to functions for the orchestrator
AVAILABLE_TOOLS = {
    "book_ride": book_ride,
    "order_food": order_food,
    "check_calendar": check_calendar
}
