FROM golang:1.26.4-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build
COPY . .
RUN go mod download && go mod verify
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w \
      -X main.Version=${VERSION} \
      -X main.Commit=${COMMIT} \
      -X main.BuildDate=${BUILD_DATE}" \
    -o trace ./cmd/trace

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tini git && \
    adduser -D -u 1000 trace

COPY --from=builder /build/trace /usr/local/bin/trace
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

USER trace
ENTRYPOINT ["tini", "--", "trace"]
CMD ["--help"]
