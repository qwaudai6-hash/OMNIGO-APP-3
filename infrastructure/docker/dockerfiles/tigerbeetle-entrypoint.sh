#!/bin/sh
set -e

# Data directory and file
DATA_DIR="/data"
DATA_FILE="${DATA_DIR}/0_0.tigerbeetle"

# Ensure data directory exists
mkdir -p "$DATA_DIR"

# If the data file doesn't exist, we need to format it
if [ ! -f "$DATA_FILE" ]; then
    echo "TigerBeetle data file not found at $DATA_FILE."
    echo "Formatting new database..."
    /tigerbeetle format --cluster=0 --replica=0 --replica-count=1 "$DATA_FILE"
    echo "Format complete."
else
    echo "TigerBeetle data file found at $DATA_FILE."
fi

# Start the TigerBeetle replica
echo "Starting TigerBeetle server on 0.0.0.0:3000..."
exec /tigerbeetle start --addresses=0.0.0.0:3000 "$DATA_FILE"
