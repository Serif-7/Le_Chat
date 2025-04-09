# Start with a Go base image
FROM golang:1.24-alpine AS builder

# Install git and build dependencies
RUN apk add --no-cache git gcc musl-dev

# Set working directory (use a container path, not your local path)
WORKDIR /build

# Copy go.mod and go.sum first to leverage Docker cache
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the code
COPY . .

# Build the application
RUN go build -o ssh-server main.go

# Create a smaller runtime image
FROM alpine:latest

# Set working directory for the runtime container
WORKDIR /app

# Create directory for SSH keys
RUN mkdir -p .ssh

# Copy the binary from the builder stage
COPY --from=builder /build/ssh-server /app/

# Generate SSH host key if not provided at runtime
RUN apk add --no-cache openssh-keygen && \
    ssh-keygen -t ed25519 -f .ssh/id_ed25519 -N "" && \
    apk del openssh-keygen

# Expose the SSH port
EXPOSE 8888

# Command to run the application
CMD ["/app/ssh-server"]


# build image: docker build -t ssh-chat .
# run image: docker run -d -p 8888:8888 --name chat-server ssh-chat
