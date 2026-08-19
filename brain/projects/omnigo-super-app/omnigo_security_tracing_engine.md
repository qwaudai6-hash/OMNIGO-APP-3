# OMNIGO Security Tracing & Audit Engine

> [!TIP]
> **Obsidian Compatibility:** Paste this code block directly into Obsidian to view the interactive Sequence Diagram of our elite Correlation Trace ID workflow.

## 1. Structural Architecture Diagram (Data Mesh & Tracing)

This block diagram represents the physical and logical architecture of the Admin Security Engine. It shows how the request crosses the security boundary, binds the Trace ID, and fans out concurrently to multiple database systems.

```mermaid
flowchart TD
    classDef client fill:#2D3748,stroke:#4A5568,color:#fff,stroke-width:2px,border-radius:10px;
    classDef gw fill:#E53E3E,stroke:#9B2C2C,color:#fff,stroke-width:2px,border-radius:10px;
    classDef engine fill:#3182CE,stroke:#2B6CB0,color:#fff,stroke-width:2px,border-radius:10px;
    classDef ctx fill:#D69E2E,stroke:#B7791F,color:#fff,stroke-width:2px,border-radius:10px;
    classDef db fill:#38A169,stroke:#2F855A,color:#fff,stroke-width:2px,border-radius:10px;
    classDef log fill:#805AD5,stroke:#6B46C1,color:#fff,stroke-width:2px,border-radius:10px;

    Admin("🖥️ Admin Flutter Dashboard\n(Client Request)"):::client
    
    subgraph API_Layer [API Security Boundary]
        GinMw("🛡️ Gin Security Middleware\n(Extract or Generate X-Trace-Id)"):::gw
    end

    subgraph Core_Engine [Go Surveillance Core]
        Context("📦 Golang context.Context\n(Carries TraceID in memory)"):::ctx
        Repo("🔍 AdminSurveillanceService\n(Dual-Read Dispatcher)"):::engine
    end
    
    subgraph Storage_Mesh [Heterogeneous Data Mesh]
        PG[("🐘 PostgreSQL Replicas\n(Relational Truth via PgBouncer)")]:::db
        Neo[("🕸️ Neo4j Database\n(Topological Fraud Graph)")]:::db
    end

    AuditLog("📜 Security Audit Logger\n(Standard Out / ELK Stack)"):::log

    Admin -- "GET /api/admin/lineage" --> GinMw
    GinMw -- "Log Boundary Hit" --> AuditLog
    GinMw -- "Inject TraceID" --> Context
    Context -. "Wrap Request" .-> Repo
    
    Repo -- "Log Sweep Initiation" --> AuditLog
    
    Repo -- "Flat JOIN Query\n(Goroutine 1)" --> PG
    Repo -- "Cypher MATCH\n(Goroutine 2)" --> Neo
    
    PG -- "Relational Result" --> Repo
    Neo -- "Graph Result" --> Repo
    
    Repo -- "Desync / Match Log" --> AuditLog
    Repo -- "JSON Response" --> Admin
```

---

## 2. Security Audit Execution Workflow (Sequence Diagram)

This sequence diagram illustrates exactly how the `trace_id` is generated at the network edge, bound to the Golang `context.Context`, and propagated downwards to ensure every single database query and security validation is heavily audited.

```mermaid
sequenceDiagram
    autonumber
    
    participant Admin as Admin Dashboard (Flutter)
    participant Middleware as Gin Security Middleware
    participant Service as AdminSurveillanceService (Go)
    participant PG as PostgreSQL (Replicas)
    participant Neo4j as Neo4j Graph DB
    participant Logger as Security Audit Console

    Admin->>Middleware: GET /api/admin/lineage/ORD-9921
    
    Note over Middleware: 1. Extract X-Trace-Id Header<br/>2. OR Generate SEC-TRC-<UUID>
    Middleware->>Logger: [SECURITY-AUDIT] Admin Access Intercepted (TraceID)
    
    Note over Middleware,Service: Context Propagation: context.WithValue(ctx, "trace_id", traceID)
    Middleware->>Service: Passes Context & Calls Repository Handler
    
    Service->>Logger: [SECURITY-AUDIT] INITIATING E2E Lineage Sweep (TraceID)
    
    par Concurrent Dual-Read Audit
        Service->>PG: Relational Query (orders + stores + deliveries)
        PG-->>Service: Snapshot Results
    and
        Service->>Neo4j: Cypher Query: (c:Customer)-[:ORDERED]->(o:Order)
        Neo4j-->>Service: Topological Chain Results
    end
    
    alt Graph completely matches Relational Data
        Service->>Logger: [SECURITY-AUDIT] Graph chain topologically verified (TraceID)
    else Fraud or Missing Node Detected
        Service->>Logger: [SECURITY-CRITICAL] GRAPH DESYNC DETECTED! (TraceID)
    end
    
    Service-->>Admin: HTTP 200 OK (AdminLineageReport JSON)
```

---

## 💻 Elite Architectural Concepts Used in Code:

> [!IMPORTANT] 1. Golang Context Propagation (`context.Context`)
> Instead of using global variables which cause severe race conditions in highly concurrent environments, we utilized Go's native `context` package. By using `context.WithValue()`, the Trace ID becomes an invisible metadata backpack that travels strictly with that specific HTTP request lifecycle. Even if 100,000 admins request data simultaneously, their Trace IDs will never collide.

> [!IMPORTANT] 2. Gin Middleware Interception
> We placed a barrier (Middleware) directly in front of the HTTP router (`r.Use()`). No request can touch the database or internal services without first being stripped, inspected, and stamped with a unique `SEC-TRC-` UUID by the logger.

> [!IMPORTANT] 3. Concurrent Dual-Read (Par Block)
> The `AdminSurveillanceService` does not wait for Postgres to finish before querying Neo4j. It represents a highly advanced Scatter-Gather pattern where both heterogeneous databases are queried asynchronously. If the topological chain (Neo4j) is broken but the relational record (Postgres) exists, the system immediately flags a `SECURITY-CRITICAL` fraud vector.
