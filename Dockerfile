# Multi-stage build for kacho-nlb (api-server + migrator).
# Build context = parent dir (kacho-workspace/project/), so sibling repos
# kacho-corelib и kacho-proto доступны для replace-директив go.mod.
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY kacho-corelib /src/kacho-corelib
COPY kacho-proto   /src/kacho-proto
COPY kacho-nlb     /src/kacho-nlb

WORKDIR /src/kacho-nlb
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/kacho-loadbalancer ./cmd/kacho-loadbalancer
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/migrator           ./cmd/migrator

FROM mirror.gcr.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/kacho-loadbalancer /usr/local/bin/kacho-loadbalancer
COPY --from=builder /out/migrator           /usr/local/bin/migrator
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-loadbalancer"]
