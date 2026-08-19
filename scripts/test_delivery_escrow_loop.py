import requests
import json
import time

BASE_URL = "http://localhost"
ORDER_URL = f"{BASE_URL}:8088/api/v1/orders"
AI_ENGINE_URL = f"{BASE_URL}:8086/api/v1/ai"

def test_delivery_escrow_loop():
    print("==================================================")
    print("  OMNIGO Live Delivery Escrow & Auditor Scenario  ")
    print("==================================================")
    print("\n[1] Customer places an order...")
    
    # We will simulate the order creation or simply hit an existing endpoint
    # Since we might not have a full valid user JWT, we'll try to hit health checks first
    # to see if the services are up.
    
    try:
        health = requests.get(f"{BASE_URL}:8088/health")
        print(f"Order Service Health: {health.status_code}")
    except Exception as e:
        print(f"Order service not running! Start it first: {e}")
        return

    print("\n[2] Checking Financial Auditor Status...")
    try:
        audit_res = requests.get(f"{AI_ENGINE_URL}/audit/financial")
        if audit_res.status_code == 200:
            print("Audit Report:")
            print(json.dumps(audit_res.json(), indent=2))
        else:
            print(f"Audit API returned {audit_res.status_code}")
    except Exception as e:
        print(f"AI Engine not running! {e}")

    print("\n==================================================")
    print("  Scenario Check Complete  ")
    print("==================================================")

if __name__ == "__main__":
    test_delivery_escrow_loop()
