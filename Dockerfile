FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY ./src .
RUN go build -o gossip-server .

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /app/gossip-server /app/

ENV JOYRIDE_PORT=4709
EXPOSE $JOYRIDE_PORT

CMD ["/app/gossip-server"]