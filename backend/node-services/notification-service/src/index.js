require('dotenv').config();
const { Kafka } = require('kafkajs');
const express = require('express');
const { Pool } = require('pg');
const Redis = require('ioredis');

// ── Configuration ───────────────────────────────────────────────
const PORT = process.env.PORT || 8089;
const KAFKA_BROKERS = (process.env.KAFKA_BROKERS || 'localhost:9092').split(',');
const DB_DSN =
  process.env.DB_DSN ||
  process.env.DATABASE_URL;
if (!DB_DSN) {
  throw new Error('FATAL: DATABASE_URL (or DB_DSN) is required. Refusing to start with localhost fallback.');
}
const REDIS_ADDRS = process.env.REDIS_ADDRS;
if (!REDIS_ADDRS) {
  throw new Error('FATAL: REDIS_ADDRS is required. Refusing to start with localhost fallback.');
}

// ── Redis Initialization ─────────────────────────────────────────
let redisClient = null;
try {
  const nodes = REDIS_ADDRS.split(',').map((addr) => {
    const [host, port] = addr.split(':');
    return { host, port: parseInt(port || 6379, 10) };
  });

  if (nodes.length > 1) {
    redisClient = new Redis.Cluster(nodes);
    console.log('[Redis] Initialized Redis Cluster client for geo-queries');
  } else {
    redisClient = new Redis(nodes[0]);
    console.log('[Redis] Initialized Single Redis client for geo-queries');
  }
} catch (err) {
  console.error('[Redis] Failed to initialize client:', err.message);
}

// ── FCM Initialization (graceful degradation) ──────────────────
let firebaseAdmin = null;
let fcmInitialized = false;

if (process.env.FCM_SERVICE_ACCOUNT_PATH) {
  try {
    firebaseAdmin = require('firebase-admin');
    firebaseAdmin.initializeApp({
      credential: firebaseAdmin.credential.cert(
        require(process.env.FCM_SERVICE_ACCOUNT_PATH)
      ),
    });
    fcmInitialized = true;
    console.log('[FCM] Firebase Admin SDK initialized successfully');
  } catch (err) {
    console.warn(
      `[FCM] Failed to initialize Firebase Admin: ${err.message}. Running in SANDBOX mode (console.log only).`
    );
  }
} else {
  console.warn(
    '[FCM] No FCM_SERVICE_ACCOUNT_PATH set. Running in SANDBOX mode (console.log only).'
  );
}

// ── PostgreSQL Pool (device token lookup) ──────────────────────
const pgPool = new Pool({ connectionString: DB_DSN });
pgPool.on('error', (err) => {
  console.error('[DB] Unexpected pool error:', err.message);
});

async function getDeviceTokens(userTrackingId) {
  const result = await pgPool.query(
    'SELECT fcm_token FROM device_tokens WHERE user_tracking_id = $1 AND is_active = true',
    [userTrackingId]
  );
  return result.rows.map((r) => r.fcm_token);
}

async function getCustomerTrackingIdForOrder(orderTrackingId) {
  // The orders table uses `order_tracking_id` as its public tracking column
  // (not `tracking_id`). The previous SELECT against `tracking_id` returned
  // zero rows, so the rider-assigned / status notifications never went out.
  const result = await pgPool.query(
    'SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1',
    [orderTrackingId]
  );
  return result.rows.length > 0 ? result.rows[0].customer_tracking_id : null;
}

async function getOrderTrackingIdForGig(gigTrackingId) {
  const result = await pgPool.query(
    'SELECT order_tracking_id FROM deliveries WHERE tracking_id = $1',
    [gigTrackingId]
  );
  return result.rows.length > 0 ? result.rows[0].order_tracking_id : null;
}

// ── Notification Router ─────────────────────────────────────────
// Maps Kafka events to notification recipients + messages.
function buildNotifications(topic, payload) {
  switch (topic) {
    case 'orders.created':
      return [
        {
          recipientId: payload.user_tracking_id,
          title: 'Order Confirmed',
          body: `Your order ${payload.order_id} has been placed. Total: ${payload.total_amount} ${payload.currency || 'PKR'}`,
          orderTrackingId: payload.order_id,
        },
        {
          recipientId: payload.vendor_store_tracking_id,
          title: 'New Order Received',
          body: `New order ${payload.order_id} received. Please prepare for pickup.`,
        },
      ];

    case 'deliveries.status_updated': {
      const statusMessages = {
        accepted: { title: 'Rider Assigned', body: 'Your rider is on the way to pick up your order.' },
        picked_up: { title: 'Order Picked Up', body: 'Your order has been picked up and is en route.' },
        in_transit: { title: 'Order in Transit', body: 'Your order is on the way to you!' },
        completed: { title: 'Order Delivered', body: 'Your order has been delivered. Enjoy!' },
        failed: { title: 'Delivery Issue', body: 'There was an issue with your delivery. Please contact support.' },
      };
      const msg = statusMessages[payload.status];
      if (!msg) return [];
      return [
        {
          recipientId: null, // resolved via order lookup below
          title: msg.title,
          body: msg.body,
          orderTrackingId: payload.order_tracking_id,
        },
      ];
    }

    case 'deliveries.accepted':
      // Fired when a rider accepts a gig. Notify:
      //   1. The customer — "rider is on the way to pickup"
      //   2. The vendor — "rider is coming to hand over"
      return [
        {
          recipientId: null, // resolved via deliveries → orders lookup
          title: 'Rider Assigned',
          body: 'A rider has accepted your order and is on the way to pick it up.',
          orderTrackingId: null, // resolved via gig → order lookup
          gigTrackingId: payload.tracking_id,
        },
      ];

    case 'deliveries.broadcasted':
      return [
        {
          recipientId: 'ALL_NEARBY_RIDERS',
          title: 'New Delivery Gig Available',
          body: `Gig ${payload.tracking_id} is available for pickup near you.`,
          pickupLat: payload.pickup_lat,
          pickupLng: payload.pickup_lng,
        },
      ];

    case 'ride.requested':
      return [
        {
          recipientId: 'ALL_NEARBY_RIDERS',
          title: 'New Ride Request',
          body: `A customer needs a ride. Vehicle: ${payload.vehicle_type}, Fare: ${payload.fare_amount} PKR`,
          pickupLat: payload.pickup_lat,
          pickupLng: payload.pickup_lng,
        },
      ];

    case 'payments.wallet.completed':
      return [
        {
          recipientId: null,
          title: 'Payment Received',
          body: `Payment of ${(payload.amount_cents || 0) / 100} PKR received for order ${payload.order_id}.`,
          orderTrackingId: payload.order_id,
        },
      ];

    case 'orders.refunded':
      return [
        {
          recipientId: null,
          title: 'Refund Processed',
          body: `Your refund for order ${payload.order_id} has been processed.`,
          orderTrackingId: payload.order_id,
        },
      ];

    case 'orders.cancelled':
      return [
        {
          recipientId: null,
          title: 'Order Cancelled',
          body: `Your order ${payload.order_id} has been cancelled.`,
          orderTrackingId: payload.order_id,
        },
      ];

    case 'orders.updated':
    case 'orders.status_updated': {
      const statusMessages = {
        accepted: { title: 'Order Accepted', body: `Your order ${payload.order_id || ''} has been accepted by the merchant.` },
        shipped: { title: 'Order Dispatched', body: `Your order ${payload.order_id || ''} has been dispatched for delivery!` },
        delivered: { title: 'Order Delivered', body: `Your order ${payload.order_id || ''} has been delivered successfully!` },
        completed: { title: 'Order Completed', body: `Your order ${payload.order_id || ''} is complete. Thank you!` },
        cancelled: { title: 'Order Cancelled', body: `Order ${payload.order_id || ''} was cancelled.` },
      };
      const msg = statusMessages[payload.status];
      if (!msg) return [];
      return [
        {
          recipientId: payload.customer_tracking_id || null,
          title: msg.title,
          body: msg.body,
          orderTrackingId: payload.order_id || payload.order_tracking_id,
        },
      ];
    }

    default:
      return [];
  }
}

// ── FCM Send with sandbox fallback ──────────────────────────────
async function sendPushNotification(tokens, title, body) {
  if (tokens.length === 0) {
    console.log('[FCM] No device tokens for recipient. Skipping.');
    return;
  }

  if (fcmInitialized && firebaseAdmin) {
    try {
      const response = await firebaseAdmin.messaging().sendEachForMulticast({
        notification: { title, body },
        tokens,
      });
      console.log(
        `[FCM] Sent to ${response.successCount} devices, ${response.failureCount} failures`
      );
    } catch (err) {
      console.error(`[FCM] Send failed: ${err.message}`);
    }
  } else {
    console.log(
      `[FCM SANDBOX] -> tokens:${tokens.length} | Title: "${title}" | Body: "${body}"`
    );
  }
}

// ── Kafka Consumer ──────────────────────────────────────────────
const kafka = new Kafka({
  clientId: 'notification-service',
  brokers: KAFKA_BROKERS,
});
const consumer = kafka.consumer({ groupId: 'notification-group' });

async function runKafkaConsumer() {
  await consumer.connect();
  console.log('[Notification] Connected to Kafka');

  await consumer.subscribe({ topic: 'orders.created', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.status_updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'deliveries.status_updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'deliveries.accepted', fromBeginning: false });
  await consumer.subscribe({ topic: 'deliveries.broadcasted', fromBeginning: false });
  await consumer.subscribe({ topic: 'ride.requested', fromBeginning: false });
  await consumer.subscribe({ topic: 'payments.wallet.completed', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.refunded', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.cancelled', fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ topic, partition, message }) => {
      try {
        const payload = JSON.parse(message.value.toString());
        console.log(
          `[Notification] Received [${topic}]:`,
          JSON.stringify(payload).slice(0, 200)
        );

        const notifications = buildNotifications(topic, payload);

        for (const notif of notifications) {
          let tokens = [];

          if (notif.recipientId === 'ALL_NEARBY_RIDERS') {
            if (redisClient && notif.pickupLat && notif.pickupLng) {
              try {
                // Find all riders within 5 km of the pickup location
                const riderIDs = await redisClient.georadius(
                  'riders:live:gps',
                  notif.pickupLng,
                  notif.pickupLat,
                  5,
                  'km'
                );
                console.log(`[FCM] Found ${riderIDs.length} nearby riders for broadcast:`, riderIDs);

                if (riderIDs.length > 0) {
                  let fcmTokens = [];
                  for (const riderID of riderIDs) {
                    const tokens = await getDeviceTokens(riderID);
                    fcmTokens.push(...tokens);
                  }
                  if (fcmTokens.length > 0) {
                    await sendPushNotification(fcmTokens, notif.title, notif.body);
                  }
                }
              } catch (err) {
                console.error(`[Redis] GEORADIUS lookup failed: ${err.message}`);
              }
            } else {
              console.log(
                '[FCM] Broadcast to nearby riders skipped: Redis client or coordinates missing.'
              );
            }
            continue;
          }

          if (notif.recipientId) {
            tokens = await getDeviceTokens(notif.recipientId);
          } else if (notif.orderTrackingId) {
            const customerId = await getCustomerTrackingIdForOrder(
              notif.orderTrackingId
            );
            if (customerId) {
              tokens = await getDeviceTokens(customerId);
            }
          } else if (notif.gigTrackingId) {
            // deliveries.accepted events don't carry the order tracking id
            // directly — resolve it via the deliveries table.
            const orderTrackingId = await getOrderTrackingIdForGig(
              notif.gigTrackingId
            );
            if (orderTrackingId) {
              const customerId = await getCustomerTrackingIdForOrder(
                orderTrackingId
              );
              if (customerId) {
                tokens = await getDeviceTokens(customerId);
              }
            }
          }

          await sendPushNotification(tokens, notif.title, notif.body);
        }
      } catch (err) {
        console.error(`[Notification] Error processing message: ${err.message}`);
      }
    },
  });
}

// ── Express health endpoint ─────────────────────────────────────
const app = express();
app.get('/health', (req, res) =>
  res.json({
    status: 'ok',
    service: 'notification-service',
    fcm: fcmInitialized ? 'active' : 'sandbox',
  })
);

app.listen(PORT, () => {
  console.log(
    `[Notification] Service running on port ${PORT} (FCM: ${fcmInitialized ? 'ACTIVE' : 'SANDBOX'})`
  );
  runKafkaConsumer().catch(console.error);
});