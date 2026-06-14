# syntax=docker/dockerfile:1
# Stage 1: build
ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM golang:1.26 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w" -o /out/ccquota ./cmd/ccquota

# Stage 2: runtime
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/ccquota /ccquota

ENV CCQUOTA_DB=/data/ccquota.db

VOLUME /data

EXPOSE 11451

ENTRYPOINT ["/ccquota"]
CMD ["serve", "--addr", ":11451"]
