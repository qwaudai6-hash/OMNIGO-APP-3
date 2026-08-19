import yaml
import sys

with open("infrastructure/helm/omnigo/values.yaml", "r") as f:
    data = yaml.safe_load(f)

# Update AI Engine port
if "aiEngine" in data.get("services", {}):
    data["services"]["aiEngine"]["port"] = 8086

# Services to remove
services_to_remove = [
    "authService",
    "productService",
    "orderService",
    "deliveryGigService",
    "adminService",
    "websocketGateway"
]

for s in services_to_remove:
    if s in data.get("services", {}):
        del data["services"][s]

# Add Monolith
if "services" not in data:
    data["services"] = {}

data["services"]["monolithService"] = {
    "replicaCount": 1,
    "image": "omnigo-monolith:latest",
    "port": 8000,
    "resources": {
        "limits": {
            "cpu": "1000m",
            "memory": "1Gi"
        },
        "requests": {
            "cpu": "250m",
            "memory": "256Mi"
        }
    }
}

class MyDumper(yaml.Dumper):
    def increase_indent(self, flow=False, indentless=False):
        return super(MyDumper, self).increase_indent(flow, False)

with open("infrastructure/helm/omnigo/values.yaml", "w") as f:
    yaml.dump(data, f, sort_keys=False, Dumper=MyDumper, default_flow_style=False)

print("Successfully updated values.yaml")
