FROM rust:1.80-slim-bookworm AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y pkg-config libssl-dev cmake

WORKDIR /usr/src/app
# COPY . .
# RUN cargo build --release

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# COPY --from=builder /usr/src/app/target/release/service-name /app/server
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/app/server"]
