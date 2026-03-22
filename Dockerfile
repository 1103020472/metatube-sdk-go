# 用完整 golang 镜像，不要用 alpine（你项目依赖必须用完整版）
FROM golang:1.23 AS builder

WORKDIR /src
COPY . /src

# 下载完整依赖
RUN go mod tidy

# 直接编译官方入口（不用make！）
RUN CGO_ENABLED=0 GOOS=linux go build -o metatube-server ./cmd/server

# 运行阶段
FROM alpine:latest
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.source="https://github.com/1103020472/metatube-sdk-go"

COPY --from=builder /src/metatube-server /

RUN apk add --update --no-cache ca-certificates tzdata

ENV GIN_MODE=release
ENV PORT=8080
ENV TOKEN=""
ENV DSN=""

EXPOSE 8080
ENTRYPOINT ["/metatube-server"]