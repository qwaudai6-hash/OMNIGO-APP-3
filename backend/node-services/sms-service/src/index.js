require('dotenv').config();
const { Kafka } = require('kafkajs');
const express = require('express');
const axios = require('axios');
const { Pool } = require('pg');
const twilio = require('twilio');

// ── Configuration ───────────────────────────────────────────────
const PORT = process.env.PORT || 8091;
const KAFKA_BROKERS = (process.env.KAFKA_BROKERS || 'localhost:9092').split(',');
const DB_DSN = process.env.DB_DSN || process.env.DATABASE_URL;
if (!DB_DSN) {
  throw new Error('FATAL: DATABASE_URL (or DB_DSN) is required.');
}

// Pakistan local SMS gateway credentials (primary)
const SMS_GATEWAY_TYPE = (process.env.SMS_GATEWAY_TYPE || '').toLowerCase(); // 'twilio', 'dojatec', 'sms4connect', etc.
const SMS_GATEWAY_URL = process.env.SMS_GATEWAY_URL || '';
const SMS_GATEWAY_USERNAME = process.env.SMS_GATEWAY_USERNAME || '';
const SMS_GATEWAY_PASSWORD = process.env.SMS_GATEWAY_PASSWORD || '';
const SMS_GATEWAY_SENDER = process.env.SMS_GATEWAY_SENDER || 'OMNIGO';

// Twilio fallback credentials
const TWILIO_ACCOUNT_SID = process.env.TWILIO_ACCOUNT_SID || '';
const TWILIO_AUTH_TOKEN = process.env.TWILIO_AUTH_TOKEN || '';
const TWILIO_PHONE_NUMBER = process.env.TWILIO_PHONE_NUMBER || '';

let twilioClient = null;
if (TWILIO_ACCOUNT_SID && TWILIO_AUTH_TOKEN) {
  twilioClient = twilio(TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN);
}

const pgPool = new Pool({ connectionString: DB_DSN });
pgPool.on('error', (err) => {
  console.error('[DB] Unexpected pool error:', err.message);
});

function normalizePakistanNumber(phone) {
  if (!phone) return null;
  let cleaned = phone.replace(/\D/g, '');
  if (cleaned.startsWith('92')) return '+' + cleaned;
  if (cleaned.startsWith('0')) return '+92' + cleaned.slice(1);
  if (cleaned.startsWith('3')) return '+92' + cleaned;
  return '+' + cleaned;
}

async function fetchUserPhone(userTrackingId) {
  const result = await pgPool.query(
    'SELECT phone FROM users WHERE tracking_id = $1',
    [userTrackingId]
  );
  return result.rows.length > 0 ? normalizePakistanNumber(result.rows[0].phone) : null;
}

async function fetchCustomerPhoneByOrder(orderTrackingId) {
  const result = await pgPool.query(
    'SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1',
    [orderTrackingId]
  );
  if (result.rows.length === 0) return null;
  return fetchUserPhone(result.rows[0].customer_tracking_id);
}

async function sendViaLocalGateway(phone, message) {
  if (!SMS_GATEWAY_URL || !SMS_GATEWAY_USERNAME || !SMS_GATEWAY_PASSWORD) {
    throw new Error('Local SMS gateway not configured');
  }

  // Generic HTTP GET/POST interface for Pakistan gateways.
  // Extend per-provider payloads as needed.
  const payload = {
    username: SMS_GATEWAY_USERNAME,
    password: SMS_GATEWAY_PASSWORD,
    sender: SMS_GATEWAY_SENDER,
    to: phone,
    message,
  };

  try {
    const response = await axios.post(SMS_GATEWAY_URL, payload, { timeout: 15000 });
    console.log(`[SMS] Local gateway sent to ${phone}: ${response.status}`);
    return { success: true, provider: SMS_GATEWAY_TYPE, response: response.data };
  } catch (err) {
    console.error(`[SMS] Local gateway failed: ${err.message}`);
    throw err;
  }
}

async function sendViaTwilio(phone, message) {
  if (!twilioClient || !TWILIO_PHONE_NUMBER) {
    throw new Error('Twilio not configured');
  }
  try {
    const response = await twilioClient.messages.create({
      body: message,
      from: TWILIO_PHONE_NUMBER,
      to: phone,
    });
    console.log(`[SMS] Twilio sent to ${phone}: ${response.sid}`);
    return { success: true, provider: 'twilio', sid: response.sid };
  } catch (err) {
    console.error(`[SMS] Twilio failed: ${err.message}`);
    throw err;
  }
}

async function sendSMS(phone, message) {
  if (!phone) {
    console.log('[SMS] No phone number, skipping.');
    return { success: false, skipped: true };
  }

  // Primary: Pakistan local gateway
  if (SMS_GATEWAY_TYPE && SMS_GATEWAY_TYPE !== 'twilio') {
    try {
      return await sendViaLocalGateway(phone, message);
    } catch (err) {
      console.log('[SMS] Falling back to Twilio...');
    }
  }

  // Fallback: Twilio
  try {
    return await sendViaTwilio(phone, message);
  } catch (err) {
    console.error(`[SMS] Failed to send SMS to ${phone}: ${err.message}`);
    return { success: false, error: err.message };
  }
}

function buildSMS(topic, payload) {
  switch (topic) {
    case 'orders.created':
      return {
        phone: payload.customer_phone ? normalizePakistanNumber(payload.customer_phone) : null,
        message: `Your OMNIGO order ${payload.order_id} has been placed. Total: ${payload.total_amount} ${payload.currency || 'PKR'}.`,
      };

    case 'orders.cancelled':
      return {
        orderTrackingId: payload.order_id,
        message: `Your OMNIGO order ${payload.order_id} has been cancelled. Reason: ${payload.reason || 'N/A'}.`,
      };

    case 'orders.refunded':
      return {
        orderTrackingId: payload.order_id,
        message: `Refund of ${payload.amount} ${payload.currency || 'PKR'} has been processed for order ${payload.order_id}.`,
      };

    case 'deliveries.status_updated': {
      const statusMessages = {
        accepted: 'A rider has been assigned to your order.',
        picked_up: 'Your order has been picked up and is on the way.',
        in_transit: 'Your order is in transit.',
        completed: 'Your order has been delivered. Thank you for using OMNIGO!',
        failed: 'There was an issue with your delivery. Support will contact you.',
      };
      const statusText = statusMessages[payload.status];
      if (!statusText) return null;
      return {
        orderTrackingId: payload.order_tracking_id,
        message: `OMNIGO Update: ${statusText}`,
      };
    }

    case 'ride.requested':
      return {
        phone: payload.customer_phone ? normalizePakistanNumber(payload.customer_phone) : null,
        message: `Your OMNIGO ride has been requested. Vehicle: ${payload.vehicle_type}. Fare: ${payload.fare_amount} PKR.`,
      };

    case 'orders.updated':
    case 'orders.status_updated':
      return {
        orderTrackingId: payload.order_id || payload.order_tracking_id,
        message: `OMNIGO Update: Order ${payload.order_id || payload.order_tracking_id || ''} status updated to ${payload.status || 'processed'}.`,
      };

    case 'payments.wallet.completed':
      return {
        orderTrackingId: payload.order_id,
        message: `Payment of ${payload.amount_cents / 100} PKR received for order ${payload.order_id}.`,
      };

    default:
      return null;
  }
}

// ── Kafka Consumer ──────────────────────────────────────────────
const kafka = new Kafka({ clientId: 'sms-service', brokers: KAFKA_BROKERS });
const consumer = kafka.consumer({ groupId: 'sms-group' });

async function runKafkaConsumer() {
  await consumer.connect();
  console.log('[SMS] Connected to Kafka');

  await consumer.subscribe({ topic: 'orders.created', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.status_updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.cancelled', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.refunded', fromBeginning: false });
  await consumer.subscribe({ topic: 'deliveries.status_updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'ride.requested', fromBeginning: false });
  await consumer.subscribe({ topic: 'payments.wallet.completed', fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ topic, message }) => {
      try {
        const payload = JSON.parse(message.value.toString());
        // Log without exposing PII such as phone numbers.
        const logPayload = { ...payload };
        if (logPayload.customer_phone) logPayload.customer_phone = '[REDACTED]';
        if (logPayload.phone) logPayload.phone = '[REDACTED]';
        console.log(`[SMS] Received [${topic}]:`, JSON.stringify(logPayload).slice(0, 200));

        const sms = buildSMS(topic, payload);
        if (!sms) return;

        let phone = sms.phone;
        if (!phone && sms.orderTrackingId) {
          phone = await fetchCustomerPhoneByOrder(sms.orderTrackingId);
        }

        await sendSMS(phone, sms.message);
      } catch (err) {
        console.error(`[SMS] Error processing message: ${err.message}`);
      }
    },
  });
}

// ── Express API (for direct SMS sending and health) ──────────────
const app = express();
app.use(express.json());

app.get('/health', (req, res) => {
  const gatewayConfigured = !!(SMS_GATEWAY_URL && SMS_GATEWAY_USERNAME && SMS_GATEWAY_PASSWORD);
  const twilioConfigured = !!(twilioClient && TWILIO_PHONE_NUMBER);
  res.json({
    status: 'ok',
    service: 'sms-service',
    primary: gatewayConfigured ? SMS_GATEWAY_TYPE : 'not_configured',
    fallback: twilioConfigured ? 'twilio_active' : 'twilio_not_configured',
  });
});

app.post('/api/v1/sms/send', async (req, res) => {
  const { phone, message } = req.body;
  if (!phone || !message) {
    return res.status(400).json({ error: 'phone and message are required' });
  }
  const result = await sendSMS(normalizePakistanNumber(phone), message);
  res.json(result);
});

app.listen(PORT, () => {
  console.log(`[SMS] Service running on port ${PORT}`);
  runKafkaConsumer().catch(console.error);
});
