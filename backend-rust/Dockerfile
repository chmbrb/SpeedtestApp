# Backend image (Rust). Build context: backend-rust/. Same runtime layout as the Go image, so the compose files can swap one for the other.
FROM rust:1-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /src
# Dependencies first, so editing the code does not rebuild them.
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo 'fn main() {}' > src/main.rs && touch src/lib.rs && cargo build --release --locked && rm -rf src target/release/deps/speedtest* target/release/speedtest* target/release/.fingerprint/speedtest*
COPY src ./src
RUN cargo build --release --locked && cp target/release/speedtest-backend /out-server

FROM alpine:3.22
# wget and ca-certificates are only used to fetch Ookla's tool after the person running the server has accepted its terms.
RUN apk add --no-cache ca-certificates wget \
 && addgroup -S -g 10001 app && adduser -S -u 10001 -G app -h /var/lib/ookla app \
 && mkdir -p /var/lib/ookla && chown app:app /var/lib/ookla
COPY --from=build /out-server /app/server
COPY docker/entrypoint.sh /app/entrypoint.sh
ENV LISTEN_ADDR=:8080 \
    HOME=/var/lib/ookla \
    OOKLA_DIR=/var/lib/ookla \
    OOKLA_PATH=/var/lib/ookla/speedtest
USER app
VOLUME /var/lib/ookla
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
