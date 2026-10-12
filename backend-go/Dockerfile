# Backend image. Build context: backend-go/
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
# wget and ca-certificates are only used to fetch Ookla's tool after the person running the server has accepted its terms.
RUN apk add --no-cache ca-certificates wget \
 && addgroup -S -g 10001 app && adduser -S -u 10001 -G app -h /var/lib/ookla app \
 && mkdir -p /var/lib/ookla && chown app:app /var/lib/ookla
COPY --from=build /out/server /app/server
COPY docker/entrypoint.sh /app/entrypoint.sh
ENV LISTEN_ADDR=:8080 \
    HOME=/var/lib/ookla \
    OOKLA_DIR=/var/lib/ookla \
    OOKLA_PATH=/var/lib/ookla/speedtest
USER app
VOLUME /var/lib/ookla
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
