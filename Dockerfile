# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .


# Final stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/main .

# Labels
LABEL maintainer="Z2"
LABEL description="Z2 API"
LABEL version="1.0.4"

# Expose port
EXPOSE 7860

# Run the application
CMD ["./main"]