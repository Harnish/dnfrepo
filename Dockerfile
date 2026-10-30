FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /dnfrepo ./cmd/dnfrepo

# Alpine's stable repos have no createrepo_c; Fedora minimal ships it natively.
FROM registry.fedoraproject.org/fedora-minimal:42
RUN microdnf install -y --setopt=install_weak_deps=0 createrepo_c ca-certificates shadow-utils \
    && microdnf clean all \
    && useradd -u 10001 -r -s /sbin/nologin app && mkdir /data && chown app /data
USER app
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
COPY --from=build /dnfrepo /usr/local/bin/dnfrepo
HEALTHCHECK --interval=30s --timeout=3s CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["dnfrepo"]
