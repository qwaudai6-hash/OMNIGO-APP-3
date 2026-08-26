# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production Photon Geocoding Dockerfile (Railway Pro Ready)
# ──────────────────────────────────────────────────────────────
# Standalone open-source OSM geocoder & reverse search engine for Railway.

FROM openjdk:17-jre-slim

WORKDIR /data

RUN apt-get update && \
    apt-get install -y --no-install-recommends wget curl ca-certificates bash && \
    rm -rf /var/lib/apt/lists/*

# Download latest Photon release JAR
RUN wget -q -O /opt/photon.jar https://github.com/komoot/photon/releases/download/0.5.0/photon-0.5.0.jar || \
    wget -q -O /opt/photon.jar https://github.com/komoot/photon/releases/download/0.4.2/photon-0.4.2.jar

COPY infrastructure/docker/dockerfiles/photon-entrypoint.sh /photon-entrypoint.sh
RUN chmod +x /photon-entrypoint.sh

EXPOSE 2322

ENTRYPOINT ["/photon-entrypoint.sh"]
