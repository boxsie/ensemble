FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /ensemble .

FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends tor ca-certificates && \
    rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/sh -u 1000 ensemble
COPY --from=builder /ensemble /usr/local/bin/ensemble
RUN mkdir -p /data && chown ensemble:ensemble /data
USER ensemble
EXPOSE 9090
ENTRYPOINT ["ensemble", "--headless", "--data-dir", "/data", "--tor-path", "/usr/bin/tor", "--api-addr", "0.0.0.0:9090"]
