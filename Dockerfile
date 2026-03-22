# 官方原版构建方式（最稳定，不缺依赖）
FROM golang:alpine AS builder
WORKDIR /src
COPY . /src

# 安装 make + git（必须装，项目需要）
RUN apk add --update --no-cache make git

# 官方编译命令（一键解决所有依赖、包缺失问题）
RUN make server

# 运行镜像（完全保留你所有配置）
FROM alpine:latest
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.source="https://github.com/1103020472/metatube-sdk-go"

# 复制官方编译好的文件
COPY --from=builder /src/build/metatube-server .

RUN apk add --update --no-cache ca-certificates tzdata

ENV GIN_MODE=release
ENV PORT=8080
ENV TOKEN=""
ENV DSN=""

EXPOSE 8080
ENTRYPOINT ["/metatube-server"]