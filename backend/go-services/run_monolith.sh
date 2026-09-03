#!/bin/bash
export APP_ENV="production"
export PORT="8001"
export PUBLIC_BASE_URL="http://127.0.0.1:8001"
export CORS_ALLOWED_ORIGINS="http://127.0.0.1:8001,https://omnigo-app-production.up.railway.app"
export DATABASE_URL="postgresql://postgres.tzkjjewhnujdikkkxkmo:cTQcX259vKLCtzZj@aws-0-us-east-1.pooler.supabase.com:6543/postgres"
export DB_WRITER_DSN="postgresql://postgres.tzkjjewhnujdikkkxkmo:cTQcX259vKLCtzZj@aws-0-us-east-1.pooler.supabase.com:6543/postgres"
export DB_READER_DSN="postgresql://postgres.tzkjjewhnujdikkkxkmo:cTQcX259vKLCtzZj@aws-0-us-east-1.pooler.supabase.com:6543/postgres"
export JWT_ISSUER="omnigo-platform"
export JWT_SECRET_KEY="csiPLQIJqstuH6rIa6ulOdjl30RMYqwfk2cwTPoj2nAVHykMdWixUJnwVt6NovAyUDMqLoryPxQOSPM6jr6MlQ=="
# NOTE: HMAC_SECRET and INTERNAL_CALLBACK_SECRET should be set in Railway env.
# These values are for LOCAL DEVELOPMENT ONLY.
export HMAC_SECRET="${HMAC_SECRET:-$(openssl rand -base64 32)}"
export ADMIN_API_KEY_ENCRYPTION_KEY="/N6AKevpb5gqQ7TpEndfYJ9bHvBU54hQV8I2w+ealsQ="
export INTERNAL_CALLBACK_SECRET="${INTERNAL_CALLBACK_SECRET:-$HMAC_SECRET}"
export PAYFAST_MERCHANT_ID="102"
export PAYFAST_SECURED_KEY="zWHjBp2AlttNu1sK"
export PAYFAST_API_URL="https://ipguat.apps.net.pk/Ecommerce/api"
export PAYFAST_BASE_URL="https://ipguat.apps.net.pk/Ecommerce/api"
export PAYFAST_MERCHANT_NAME="OMNIGO"

killall monolith || true
nohup ./monolith > monolith.log 2>&1 &
