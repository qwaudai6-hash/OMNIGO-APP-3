"""Real data loaders — fetch live data from PostgreSQL.

Replaces the old synthetic_data.py which generated fake CSVs.
These functions query the actual OMNIGO production database for:
  - Fraud graph: real user→device edges from device_tokens + users
  - Surge RL: real order/delivery counts by hour from orders + deliveries
  - SASRec sequences: real per-user product view/purchase sequences from order_items
"""
import os
import logging
import pandas as pd
import asyncpg

logger = logging.getLogger("ai-engine.data")

DATA_DIR = os.path.join(os.path.dirname(__file__), "data")
os.makedirs(DATA_DIR, exist_ok=True)


async def load_fraud_graph_data(pool: asyncpg.Pool, num_users: int = 5000) -> tuple[pd.DataFrame, pd.DataFrame]:
    """
    Loads real user→device relationships from the users + device_tokens tables.
    Users sharing many devices with flagged users form fraud ring edges.
    """
    async with pool.acquire() as conn:
        # Real device sharing graph: which users have which device tokens
        rows = await conn.fetch("""
            SELECT
                u.tracking_id AS user_id,
                u.risk_score,
                u.verification_status,
                dt.fcm_token AS device_id
            FROM users u
            LEFT JOIN device_tokens dt ON dt.user_tracking_id = u.tracking_id
            WHERE dt.fcm_token IS NOT NULL
            ORDER BY u.id
            LIMIT $1
        """, num_users)

        if not rows:
            logger.warning("No device token data found — fraud graph will be empty")
            return pd.DataFrame(columns=["user_id", "device_id"]), pd.DataFrame(columns=["user_id", "is_fraud"])

        df_edges = pd.DataFrame(rows, columns=["user_id", "risk_score", "verification_status", "device_id"])

        # Build node labels: a user is "fraud" if risk_score > 0.7 or verification_status = 'rejected'
        nodes = df_edges.groupby("user_id").agg(
            is_fraud=("risk_score", lambda x: 1 if (x > 0.7).any() else 0),
        ).reset_index()
        nodes["is_fraud"] = nodes["is_fraud"].astype(int)

        # Deduplicate edges
        edges = df_edges[["user_id", "device_id"]].drop_duplicates()

        edges.to_csv(os.path.join(DATA_DIR, "fraud_graph_edges.csv"), index=False)
        nodes.to_csv(os.path.join(DATA_DIR, "fraud_graph_nodes.csv"), index=False)

        # Encode string IDs to integer indices for PyG
        user_map = {uid: i for i, uid in enumerate(nodes["user_id"].unique())}
        device_map = {did: i for i, did in enumerate(edges["device_id"].unique())}
        edges["user_idx"] = edges["user_id"].map(user_map)
        edges["device_idx"] = edges["device_id"].map(device_map)

        logger.info(f"Loaded fraud graph: {len(nodes)} users, {len(edges)} edges, {nodes['is_fraud'].sum()} flagged")
        return edges, nodes


async def load_surge_rl_data(pool: asyncpg.Pool, num_days: int = 30) -> pd.DataFrame:
    """
    Loads real hourly order + rider counts for surge pricing RL training.
    State = [active_orders, available_riders, hour_of_day].
    """
    async with pool.acquire() as conn:
        rows = await conn.fetch("""
            WITH hourly AS (
                SELECT
                    date_trunc('hour', created_at) AS hour_bucket,
                    COUNT(DISTINCT o.order_tracking_id) AS active_orders,
                    COUNT(DISTINCT CASE WHEN d.status IN ('assigned','accepted') THEN d.rider_tracking_id END) AS available_riders,
                    EXTRACT(HOUR FROM created_at)::int AS hour
                FROM orders o
                LEFT JOIN deliveries d ON d.order_tracking_id = o.order_tracking_id
                WHERE o.created_at > NOW() - INTERVAL '%s days'
                GROUP BY 1, 4
                ORDER BY 1
            )
            SELECT active_orders, available_riders, hour FROM hourly
        """ % num_days)

        if not rows:
            logger.warning("No order data found — surge model will use fallback heuristic")
            return pd.DataFrame(columns=["orders", "riders", "hour", "action", "reward"])

        df = pd.DataFrame(rows, columns=["active_orders", "available_riders", "hour"])

        # Compute optimal action labels: what multiplier SHOULD have been applied?
        # demand_ratio = orders / (riders + 1)
        df["demand_ratio"] = df["active_orders"] / (df["available_riders"] + 1)
        df["action"] = 0  # default 1.0x
        df.loc[df["demand_ratio"] > 1.2, "action"] = 1   # 1.5x
        df.loc[df["demand_ratio"] > 2.0, "action"] = 2   # 2.0x
        df.loc[df["demand_ratio"] > 3.0, "action"] = 3   # 2.5x

        # Reward: +1 if action matches optimal, -1 otherwise
        multipliers = [1.0, 1.5, 2.0, 2.5]
        optimal = df["demand_ratio"].apply(
            lambda r: 3 if r > 3.0 else 2 if r > 2.0 else 1 if r > 1.2 else 0
        )
        df["reward"] = (df["action"] == optimal).astype(int) * 2 - 1

        df.rename(columns={"active_orders": "orders", "available_riders": "riders"}, inplace=True)
        df.to_csv(os.path.join(DATA_DIR, "surge_rl_transitions.csv"), index=False)

        logger.info(f"Loaded surge RL data: {len(df)} hourly transitions over {num_days} days")
        return df[["orders", "riders", "hour", "action", "reward"]]


async def load_sasrec_sequences(pool: asyncpg.Pool, num_users: int = 2000, seq_len: int = 10) -> pd.DataFrame:
    """
    Loads real per-user product purchase sequences from order_items joined with orders.
    Each row = [user_id, item_0, item_1, ..., item_N] in chronological order.
    Items are encoded as integer indices from product_tracking_id.
    """
    async with pool.acquire() as conn:
        rows = await conn.fetch("""
            WITH user_orders AS (
                SELECT
                    o.customer_tracking_id AS user_id,
                    oi.product_tracking_id,
                    o.created_at
                FROM orders o
                JOIN order_items oi ON oi.order_tracking_id = o.order_tracking_id
                ORDER BY o.customer_tracking_id, o.created_at
            )
            SELECT user_id, product_tracking_id, created_at
            FROM user_orders
        """)

        if not rows:
            logger.warning("No order history found — recommendation model will return popular products")
            return pd.DataFrame()

        df = pd.DataFrame(rows, columns=["user_id", "product_tracking_id", "created_at"])

        # Encode product_tracking_id → integer index
        product_map = {pid: i + 1 for i, pid in enumerate(df["product_tracking_id"].unique())}
        df["item_idx"] = df["product_tracking_id"].map(product_map)

        # Build sequences per user (chronological)
        sequences = []
        for user_id, group in df.groupby("user_id"):
            items = group.sort_values("created_at")["item_idx"].tolist()
            # Pad or truncate to seq_len
            if len(items) < seq_len:
                items = [0] * (seq_len - len(items)) + items
            else:
                items = items[-seq_len:]
            sequences.append([user_id] + items)
            if len(sequences) >= num_users:
                break

        cols = ["user_id"] + [f"item_{i}" for i in range(seq_len)]
        seq_df = pd.DataFrame(sequences, columns=cols)
        seq_df.to_csv(os.path.join(DATA_DIR, "sasrec_sequences.csv"), index=False)

        # Save product map for decoding recommendations back to tracking IDs
        import json
        with open(os.path.join(DATA_DIR, "product_map.json"), "w") as f:
            json.dump({v: k for k, v in product_map.items()}, f)

        logger.info(f"Loaded SASRec sequences: {len(seq_df)} users, {len(product_map)} unique products")
        return seq_df