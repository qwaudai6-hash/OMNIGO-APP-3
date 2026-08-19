# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production OSRM Dockerfile (Railway Ready)
# ──────────────────────────────────────────────────────────────
# This Dockerfile automatically downloads the map PBF file,
# extracts it, partitions it, and customizes it during the image build.
# This ensures that when the container starts on Railway, it has the
# required map data built into the image directly, removing the need
# for complex volume mounts or pre-seeding scripts.

FROM osrm/osrm-backend:latest

# Define build arguments for map region (Default is Pakistan)
# You can override this in Railway during deployment if you expand to other regions.
ARG MAP_URL=http://download.geofabrik.de/asia/pakistan-latest.osm.pbf
ARG MAP_FILE=map.osm.pbf
ARG MAP_BASE=map.osrm

# Working directory inside the container where map data will reside
WORKDIR /data

# 1. Install wget to fetch the map data and ca-certificates for HTTPS
RUN apt-get update && \
    apt-get install -y wget ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# 2. Download the latest map data from Geofabrik
RUN echo "Downloading map from: ${MAP_URL}" && \
    wget -q --show-progress ${MAP_URL} -O ${MAP_FILE}

# 3. Process the map using the standard car profile
# We use MLD (Multi-Level Dijkstra) because it requires significantly less RAM
# to build and run compared to CH (Contraction Hierarchies), making it ideal
# for PaaS environments like Railway.
RUN osrm-extract -p /opt/car.lua ${MAP_FILE} && \
    osrm-partition ${MAP_BASE} && \
    osrm-customize ${MAP_BASE}

# 4. Clean up the raw PBF file to reduce the final Docker image size drastically
RUN rm ${MAP_FILE}

# Expose standard OSRM port
EXPOSE 5000

# Start the OSRM routing server using MLD algorithm
CMD ["osrm-routed", "--algorithm", "mld", "--bind", "0.0.0.0", "--port", "5000", "/data/map.osrm"]
