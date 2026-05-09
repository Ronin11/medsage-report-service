# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git

# Copy proto + authkit dependencies and go mod files
COPY proto/gen/go/ /proto/gen/go/
COPY auth/ /auth/
COPY report-service/go.mod report-service/go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY report-service/ .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /report-service .

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Copy binary from builder
COPY --from=builder /report-service .

# Run as non-root user
RUN adduser -D -g '' appuser
USER appuser

EXPOSE 8080

CMD ["./report-service"]
