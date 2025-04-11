FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY ./src .
RUN go build -o gossip-server .

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /app/gossip-server /app/
EXPOSE 9999/udp
CMD ["/app/gossip-server"]