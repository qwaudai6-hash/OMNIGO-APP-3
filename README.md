# OMNIGO Super App

An Enterprise-grade E-Commerce & Ride-Hailing platform (Super App) built with Go, Rust, Python, Node.js, and Flutter.

## Architecture Highlights
- **Backend:** Go (Business logic), Rust (Auth & WebSockets), Python (AI/ML), Node.js (Notifications).
- **Frontend:** Flutter with Leaflet Maps integration (`flutter_map`).
- **Database:** PostgreSQL (Master-Replica).
- **Cache & Real-time:** Redis Cluster.
- **Event Bus:** Apache Kafka.
- **Routing Engine:** OSRM (Open Source Routing Machine).

## Getting Started

### Prerequisites
- Docker & Docker Compose
- Make

### Quick Start
```bash
cp .env.example .env
make up
```

For more details, see the `docs/` folder.
