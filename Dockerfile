# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git

COPY report-service/go.mod report-service/go.sum* ./

# Download dependencies
# The shared modules (medsage-proto, medsage-authkit) are private, so this
# build needs a credential to fetch them. It arrives as a BuildKit secret, is
# used only for this layer, and never lands in the image — unlike a build ARG,
# which would be visible in the history.
RUN --mount=type=secret,id=modules_token \
    GOPRIVATE='github.com/Ronin11/*' \
    sh -c 'test -s /run/secrets/modules_token || { echo; echo "ERROR: MODULES_TOKEN is empty or unset."; echo "Set it in .env — a read-only token for the private module repos."; echo "See .env.example."; echo; exit 1; }; \
           git config --global url."https://x-access-token:$(cat /run/secrets/modules_token)@github.com/".insteadOf "https://github.com/" && \
           go mod download && \
           git config --global --unset-all url."https://x-access-token:$(cat /run/secrets/modules_token)@github.com/".insteadOf'
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
