FROM golang:1.22 AS builder
WORKDIR /app
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o sync-groups main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/sync-groups /usr/local/bin/sync-groups
ENTRYPOINT ["/usr/local/bin/sync-groups"]
