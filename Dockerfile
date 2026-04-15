# syntax=docker/dockerfile:1
FROM golang:1.22 AS build
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/gateway-target-prober ./main.go

FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.source="https://github.com/b1r3k/gateway-target-prober"
LABEL org.opencontainers.image.description="Ingress target prober"

USER 65532:65532
COPY --from=build /out/gateway-target-prober /gateway-target-prober
ENTRYPOINT ["/gateway-target-prober"]
