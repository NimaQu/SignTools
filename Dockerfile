# SignTools with built-in signing on Linux: the web service, signtools-portal (Apple ID login and
# provisioning, built on Impactor's plume_core) and the Linux signer (rcodesign) in one image.

FROM rust:1-bookworm AS portal
WORKDIR /src/portal
COPY portal/ ./
RUN cargo build --release --locked && cp target/release/signtools-portal /signtools-portal

FROM golang:1.26-bookworm AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY src ./src
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false -o /SignTools

FROM python:3.12-slim-bookworm
ARG RCODESIGN_VERSION=0.29.0

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip zip binutils openssl \
 && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir "cryptography>=42" "lief>=0.16"
RUN set -eux; \
    arch="$(uname -m)"; \
    case "$arch" in x86_64|aarch64) ;; *) echo "unsupported arch $arch"; exit 1;; esac; \
    curl -fsSL "https://github.com/indygreg/apple-platform-rs/releases/download/apple-codesign%2F${RCODESIGN_VERSION}/apple-codesign-${RCODESIGN_VERSION}-${arch}-unknown-linux-musl.tar.gz" \
      | tar -xz -C /tmp; \
    install -m 0755 /tmp/apple-codesign-*/rcodesign /usr/local/bin/rcodesign; \
    rm -rf /tmp/apple-codesign-*; \
    rcodesign --version

COPY --from=portal /signtools-portal /usr/local/bin/signtools-portal
COPY --from=server /SignTools /SignTools
COPY signer/sign.py /signer/sign.py
COPY signer/libsubstrate /signer/libsubstrate

# config: /signer-cfg.yml, data: /data (save_dir "data"); builder.local.sign_script defaults to signer/sign.py
WORKDIR /
ENTRYPOINT ["/SignTools"]
EXPOSE 8080
