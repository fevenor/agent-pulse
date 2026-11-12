# 仓库根目录为构建上下文；Web 先由 pnpm 构建到 web/hub/dist。
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/go
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags '-s -w' -o /out/ ./cmd/agent-pulse-hub ./cmd/agent-pulse-relay
RUN mkdir -p /data && chown 65532:65532 /data

FROM scratch AS relay
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build /out/agent-pulse-relay /agent-pulse-relay
USER 65532:65532
WORKDIR /data
ENTRYPOINT ["/agent-pulse-relay"]
CMD ["--config", "/etc/agent-pulse/relay.yaml"]

FROM scratch AS hub
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build /out/agent-pulse-hub /agent-pulse-hub
COPY web/hub/dist/ /web/
COPY deploy/docker/hub.default.yaml /etc/agent-pulse/hub.yaml
USER 65532:65532
WORKDIR /data
ENTRYPOINT ["/agent-pulse-hub"]
CMD ["--config", "/etc/agent-pulse/hub.yaml"]
