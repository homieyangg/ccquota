# syntax=docker/dockerfile:1
# Stage 1: build
ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM golang:1.26 AS builder

ARG TARGETOS
ARG TARGETARCH
# 由 release.yml 傳入 git tag,注入 main.version;不傳則維持 dev。
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/ccquota ./cmd/ccquota

# 預先建好資料目錄，讓匿名 volume 繼承 nonroot(65532) 擁有權。否則容器以
# nonroot 執行時寫不進預設 root 擁有的 /data，會報 CANTOPEN(14)。
RUN mkdir -p /out/data

# Stage 2: runtime
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/ccquota /ccquota
COPY --from=builder --chown=65532:65532 /out/data /data

ENV CCQUOTA_DB=/data/ccquota.db

VOLUME /data

EXPOSE 11451

ENTRYPOINT ["/ccquota"]
CMD ["serve", "--addr", ":11451"]
