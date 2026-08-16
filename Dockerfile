# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Dependencies are resolved in their own layer so a source-only change does not
# re-download the module graph.
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ ./

# CGO is disabled so the binary is fully static and can run on a distroless
# base with no libc at all.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/service .


# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Timezone data is required: provider timestamps arrive without an offset and
# are interpreted in the provider's zone (Africa/Lagos by default). A
# distroless image ships no tzdata, so LoadLocation would fail at startup.
COPY --from=builder /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/zoneinfo.zip
ENV ZONEINFO=/usr/local/go/lib/time/zoneinfo.zip

# The committed default settings. Nothing is defaulted in code, so the image
# carries this file; anything the environment sets overrides it.
COPY .env.example /app/.env.example

COPY --from=builder /out/service /app/service

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/app/service"]
