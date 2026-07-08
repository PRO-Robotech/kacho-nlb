# Multi-stage build for kacho-nlb (api-server + migrator).
# Single-repo build: внутренние зависимости (kacho-corelib, kacho-compute, kacho-geo,
# kacho-iam, kacho-vpc) тянутся как versioned-модули из GitHub
# (go.mod без replace), build-context — этот репо.
FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY . .

RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/kacho-loadbalancer ./cmd/kacho-loadbalancer
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/migrator           ./cmd/migrator

FROM mirror.gcr.io/library/alpine:3.20
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates
COPY --from=builder /out/kacho-loadbalancer /usr/local/bin/kacho-loadbalancer
COPY --from=builder /out/migrator           /usr/local/bin/migrator
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-loadbalancer"]
