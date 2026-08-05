# Stage 1: builder
# Uses alpine + musl-gcc to provide the CGo toolchain required by go-sqlite3,
# while keeping the build image small.
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git gcc musl-dev

WORKDIR /build

# Cache module downloads by copying manifests first
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source
COPY . .

# Build with CGo enabled so go-sqlite3 compiles against musl.
# The resulting static-ish binary runs cleanly on alpine.
RUN CGO_ENABLED=1 go build -o gitrieve main.go

# Stage 2: runtime
# alpine keeps the final image small while still providing git (needed to clone
# repos) and ca-certificates (needed for HTTPS to git servers / S3).
FROM alpine:latest

RUN apk add --no-cache ca-certificates git tzdata

WORKDIR /app

# Copy the binary
COPY --from=builder /build/gitrieve /app/gitrieve

# Copy web assets (templates + static) — server.go serves these from ./web
COPY --from=builder /build/web /app/web

EXPOSE 8080

ENTRYPOINT ["/app/gitrieve"]
CMD ["server"]
