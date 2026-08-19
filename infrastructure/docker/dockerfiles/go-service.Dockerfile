FROM golang:1.23-alpine AS builder

WORKDIR /app
# We will copy the go.mod from the specific service directory
# This is a generic template

# Cache dependencies
# COPY go.mod go.sum ./
# RUN go mod download

# Build statically linked binary
# COPY . .
# RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /server ./cmd/server/

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
# COPY --from=builder /server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
