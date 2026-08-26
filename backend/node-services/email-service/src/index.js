require('dotenv').config();
const { Kafka } = require('kafkajs');
const nodemailer = require('nodemailer');
const PDFDocument = require('pdfkit');
const express = require('express');
const { Pool } = require('pg');
const path = require('path');
const fs = require('fs');

// ── Configuration ───────────────────────────────────────────────
const PORT = process.env.PORT || 8090;
// SP-NJ-08: fail fast on missing broker config.
const KAFKA_BROKERS = process.env.KAFKA_BROKERS
  ? process.env.KAFKA_BROKERS.split(',')
  : (() => { console.error('[FATAL] KAFKA_BROKERS env var is required'); process.exit(1); })();
const DB_DSN =
  process.env.DB_DSN ||
  process.env.DATABASE_URL;
if (!DB_DSN) {
  throw new Error('FATAL: DATABASE_URL (or DB_DSN) is required. Refusing to start with localhost fallback.');
}

// ── SMTP Transporter (graceful degradation) ────────────────────
let transporter = null;
let smtpActive = false;
let smtpVerified = Promise.resolve(); // resolved when transport check done

if (process.env.SMTP_HOST) {
  transporter = nodemailer.createTransport({
    host: process.env.SMTP_HOST,
    port: parseInt(process.env.SMTP_PORT || '587'),
    auth: process.env.SMTP_USER
      ? { user: process.env.SMTP_USER, pass: process.env.SMTP_PASS }
      : undefined,
  });

  // SP-NJ-11: track verification; the HTTP listener waits on this so no
  // request lands during the window where emails would fake-succeed.
  smtpVerified = new Promise((resolve) => {
    transporter.verify((err) => {
      if (err) {
        console.warn(`[SMTP] Verification failed: ${err.message}. Running in SANDBOX mode.`);
      } else {
        smtpActive = true;
        console.log('[SMTP] Connection verified successfully');
      }
      resolve();
    });
  });
} else {
  console.warn('[SMTP] No SMTP_HOST set. Running in SANDBOX mode (console.log only).');
}

// ── PostgreSQL Pool ─────────────────────────────────────────────
const pgPool = new Pool({ connectionString: DB_DSN });
pgPool.on('error', (err) => {
  console.error('[DB] Unexpected pool error:', err.message);
});

async function fetchOrderDetails(orderTrackingId) {
  const orderResult = await pgPool.query(
    `SELECT order_tracking_id AS tracking_id, customer_tracking_id, store_tracking_id AS vendor_store_tracking_id,
            total_amount, currency, status, created_at
     FROM orders WHERE order_tracking_id = $1`,
    [orderTrackingId]
  );
  if (orderResult.rows.length === 0) return null;

  const order = orderResult.rows[0];

  // Customer email + name
  const customerResult = await pgPool.query(
    'SELECT email, full_name FROM users WHERE tracking_id = $1',
    [order.customer_tracking_id]
  );
  order.customer_email = customerResult.rows[0]?.email || null;
  order.customer_name = customerResult.rows[0]?.full_name || 'Valued Customer';

  // Store name
  const storeResult = await pgPool.query(
    'SELECT store_name FROM stores WHERE store_tracking_id = $1',
    [order.vendor_store_tracking_id]
  );
  order.store_name = storeResult.rows[0]?.store_name || 'OMNIGO Store';

  // Order line items (if order_items table exists)
  try {
    const itemsResult = await pgPool.query(
      `SELECT product_tracking_id, quantity, price_at_checkout
       FROM order_items WHERE order_tracking_id = $1`,
      [orderTrackingId]
    );
    order.items = itemsResult.rows;
  } catch (e) {
    // Only fallback to empty if table doesn't exist (Postgres error code 42P01)
    if (e.code === '42P01') {
      order.items = [];
    } else {
      console.error('[DB] Error fetching order items:', e.message);
      order.items = [];
    }
  }

  return order;
}

// ── PDF Invoice Generator ──────────────────────────────────────
function generateInvoicePDF(order) {
  return new Promise((resolve, reject) => {
    const doc = new PDFDocument({ margin: 50 });
    const buffers = [];

    doc.on('data', buffers.push.bind(buffers));
    doc.on('end', () => resolve(Buffer.concat(buffers)));
    doc.on('error', reject);

    // Header
    doc.fontSize(24).fillColor('#0f0f0f').text('OMNIGO', { align: 'center' });
    doc.fontSize(12).fillColor('#666').text('Digital Receipt', { align: 'center' });
    doc.moveDown(1.5);

    // Order info
    doc.fontSize(11).fillColor('#1a1a1a');
    doc.text(`Order ID: ${order.tracking_id}`);
    doc.text(`Date: ${new Date(order.created_at).toLocaleString()}`);
    doc.text(`Customer: ${order.customer_name}`);
    doc.text(`Store: ${order.store_name}`);
    doc.moveDown(1);

    // Items table header
    const tableTop = doc.y;
    doc.fontSize(10).fillColor('#999');
    doc.text('Product ID', 50, tableTop, { width: 200 });
    doc.text('Qty', 280, tableTop, { width: 50 });
    doc.text('Unit Price', 340, tableTop, { width: 90 });
    doc.text('Subtotal', 440, tableTop, { width: 100 });
    doc.moveTo(50, tableTop + 14).lineTo(540, tableTop + 14).strokeColor('#eee').stroke();
    doc.moveDown(1);

    // Items rows
    let y = tableTop + 22;
    doc.fontSize(10).fillColor('#1a1a1a');
    if (order.items && order.items.length > 0) {
      for (const item of order.items) {
        const subtotal = (item.price_at_checkout * item.quantity).toFixed(2);
        doc.text(item.product_tracking_id, 50, y, { width: 200 });
        doc.text(String(item.quantity), 280, y, { width: 50 });
        doc.text(`${item.price_at_checkout} ${order.currency || 'PKR'}`, 340, y, { width: 90 });
        doc.text(`${subtotal} ${order.currency || 'PKR'}`, 440, y, { width: 100 });
        y += 20;
      }
    } else {
      doc.text('Items detail not available', 50, y);
      y += 20;
    }

    // Total
    doc.moveDown(0.5);
    doc.moveTo(50, y).lineTo(540, y).strokeColor('#eee').stroke();
    y += 16;
    doc.fontSize(13).fillColor('#0f0f0f');
    doc.text(`Total: ${order.total_amount} ${order.currency || 'PKR'}`, 350, y, { width: 190, align: 'right' });

    // Footer
    doc.moveDown(3);
    doc.fontSize(9).fillColor('#999');
    doc.text('Thank you for shopping with OMNIGO!', { align: 'center' });
    doc.text('This is an automated receipt. Please do not reply.', { align: 'center' });

    doc.end();
  });
}

// ── Email Send with sandbox fallback ────────────────────────────
async function sendReceiptEmail(to, subject, text, pdfBuffer) {
  if (smtpActive && transporter) {
    const mailOptions = {
      from: '"OMNIGO" <noreply@omnigo.com>',
      to,
      subject,
      text,
      attachments: [
        {
          filename: `receipt-${Date.now()}.pdf`,
          content: pdfBuffer,
          contentType: 'application/pdf',
        },
      ],
    };
    try {
      const info = await transporter.sendMail(mailOptions);
      console.log(`[SMTP] Receipt sent to ${to}: ${info.messageId}`);
    } catch (err) {
      console.error(`[SMTP] Send failed: ${err.message}`);
    }
  } else {
    console.log(
      `[SMTP SANDBOX] -> To: ${to} | Subject: "${subject}" | PDF size: ${pdfBuffer.length} bytes`
    );
  }
}

// ── Kafka Consumer ──────────────────────────────────────────────
const kafka = new Kafka({ clientId: 'email-service', brokers: KAFKA_BROKERS });
const consumer = kafka.consumer({ groupId: 'email-group' });

async function runKafkaConsumer() {
  await consumer.connect();
  console.log('[Email] Connected to Kafka');

  // Subscribe to deliveries.status_updated, orders.created, orders.updated, and orders.status_updated
  await consumer.subscribe({ topic: 'deliveries.status_updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.created', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.updated', fromBeginning: false });
  await consumer.subscribe({ topic: 'orders.status_updated', fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ topic, message }) => {
      try {
        if (!message || !message.value) return;
        const payload = JSON.parse(message.value.toString());
        // Redact potentially sensitive fields from logged payload.
        const logPayload = { ...payload };
        if (logPayload.customer_email) logPayload.customer_email = '[REDACTED]';
        if (logPayload.phone) logPayload.phone = '[REDACTED]';
        console.log(`[Email] Received [${topic}]:`, JSON.stringify(logPayload).slice(0, 200));

        if (topic === 'orders.created' || topic === 'orders.updated' || topic === 'orders.status_updated') {
          const orderTrackingId = payload.order_id || payload.order_tracking_id || payload.tracking_id;
          if (orderTrackingId) {
            const order = await fetchOrderDetails(orderTrackingId);
            if (order && order.customer_email) {
              const statusUpper = (payload.status || order.status || 'UPDATED').toUpperCase();
              const subject = `OMNIGO Order Update: ${orderTrackingId} [${statusUpper}]`;
              const textBody = `Hi ${order.customer_name},\n\nYour OMNIGO order ${orderTrackingId} status is now: ${statusUpper}.\n\nTotal: ${order.total_amount} ${order.currency || 'PKR'}\n\nThank you for choosing OMNIGO!\n\n— OMNIGO Team`;
              await sendTextEmail(order.customer_email, subject, textBody);
            }
          }
          return;
        }

        // For delivery events, only process completed deliveries for PDF receipts
        if (payload.status !== 'completed') {
          return;
        }

        const orderTrackingId = payload.order_tracking_id;
        if (!orderTrackingId) {
          console.warn('[Email] No order_tracking_id in payload. Skipping.');
          return;
        }

        // Fetch order + customer details
        const order = await fetchOrderDetails(orderTrackingId);
        if (!order) {
          console.warn(`[Email] Order ${orderTrackingId} not found in DB. Skipping.`);
          return;
        }
        if (!order.customer_email) {
          console.warn(`[Email] No email for customer ${order.customer_tracking_id}. Skipping.`);
          return;
        }

        // Generate PDF receipt
        const pdfBuffer = await generateInvoicePDF(order);

        // Send email
        const subject = `Your OMNIGO Order Receipt — ${order.tracking_id}`;
        const textBody = `Hi ${order.customer_name},\n\nThank you for your order!\n\nOrder ID: ${order.tracking_id}\nTotal: ${order.total_amount} ${order.currency || 'PKR'}\n\nPlease find your receipt attached.\n\n— OMNIGO Team`;
        await sendReceiptEmail(order.customer_email, subject, textBody, pdfBuffer);
      } catch (err) {
        console.error(`[Email] Error processing message: ${err.message}`);
      }
    },
  });
}

// ── Express health endpoint ─────────────────────────────────────
const app = express();
app.use(express.json({ limit: '256kb' }));

app.get('/health', (req, res) =>
  res.json({
    status: 'ok',
    service: 'email-service',
    smtp: smtpActive ? 'active' : 'sandbox',
  })
);

// sendTextEmail is the generic (no-PDF) email sender used by the
// auth-service for password resets, email verification, and 2FA
// enrollment confirmations. Returns true on success.
async function sendTextEmail(to, subject, text) {
  if (smtpActive && transporter) {
    const mailOptions = {
      from: '"OMNIGO" <noreply@omnigo.com>',
      to,
      subject,
      text,
    };
    try {
      const info = await transporter.sendMail(mailOptions);
      console.log(`[SMTP] Text mail sent to ${to}: ${info.messageId}`);
      return true;
    } catch (err) {
      console.error(`[SMTP] Send failed: ${err.message}`);
      return false;
    }
  } else {
    console.log(`[SMTP SANDBOX] -> To: ${to} | Subject: "${subject}"\n${text}`);
    return true;
  }
}

// POST /send — used by the auth-service for transactional email.
// Body: { to, subject, url, app }
// The URL is appended to a body template based on the `app` field:
//   - app: "forgot-password"   → "Click here to reset: <url>"
//   - app: "verify-email"      → "Click here to verify: <url>"
//   - app: "2fa-enroll"        → "Scan the QR or enter this secret: <url>"
async function handleSendRequest(req, res) {
  const { to, subject, url, app } = req.body || {};
  if (!to || !subject || !url || !app) {
    return res.status(400).json({
      error: 'to, subject, url, app are required',
    });
  }
  let body;
  switch (app) {
    case 'forgot-password':
      body = `Hi,\n\nClick the link below to reset your OMNIGO password. The link expires in 1 hour.\n\n${url}\n\nIf you didn't request this, please ignore this email.\n\n— OMNIGO Team`;
      break;
    case 'verify-email':
      body = `Hi,\n\nClick the link below to verify your email address. The link expires in 24 hours.\n\n${url}\n\n— OMNIGO Team`;
      break;
    case '2fa-enroll':
      body = `Hi,\n\nYou have enrolled two-factor authentication on your OMNIGO account.\n\nSecret: ${url}\n\nScan this secret in Google Authenticator, Authy, or 1Password to add your account.\n\n— OMNIGO Team`;
      break;
    default:
      body = `Hi,\n\n${url}\n\n— OMNIGO Team`;
  }
  const ok = await sendTextEmail(to, subject, body);
  if (!ok) {
    return res.status(502).json({ error: 'smtp send failed' });
  }
  res.json({ status: 'sent' });
}

function authenticateInternal(req, res, next) {
  const internalSecret = process.env.INTERNAL_SERVICE_KEY || process.env.JWT_SECRET || 'omnigo-internal-service-secret';
  const authHeader = req.headers['authorization'] || req.headers['x-internal-service-key'];
  if (!authHeader) {
    return res.status(401).json({ error: 'unauthorized: missing internal auth header' });
  }
  const key = authHeader.replace(/^Bearer\s+/i, '').trim();
  if (key !== internalSecret && key !== process.env.JWT_SECRET) {
    return res.status(403).json({ error: 'forbidden: invalid internal service key' });
  }
  next();
}

app.post('/send', authenticateInternal, handleSendRequest);

// SP-NJ-11: bind the port only after SMTP state is known.
smtpVerified.finally(() => {
  app.listen(PORT, () => {
    console.log(
      `[Email] Service running on port ${PORT} (SMTP: ${smtpActive ? 'ACTIVE' : 'SANDBOX'})`
    );
    runKafkaConsumer().catch(console.error);
  });
});

const gracefulShutdown = async (signal) => {
  console.log(`[${signal}] Graceful shutdown initiated...`);
  try {
    if (consumer) await consumer.disconnect();
    if (pgPool) await pgPool.end();
    console.log('Cleanup complete. Exiting.');
    process.exit(0);
  } catch (err) {
    console.error('Shutdown error:', err);
    process.exit(1);
  }
};
process.on('SIGTERM', () => gracefulShutdown('SIGTERM'));
process.on('SIGINT', () => gracefulShutdown('SIGINT'));