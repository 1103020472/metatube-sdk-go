# 编译阶段（替换你的 make 编译）
FROM golang:alpine AS builder
WORKDIR /src
COPY . /src

# 安装依赖 + 直接编译（不使用 make，彻底解决你报错）
RUN apk add --no-cache git
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o metatube-server main.go

# 运行阶段（完全保留你所有配置）
FROM alpine:latest
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.source="https://github.com/1103020472/metatube-sdk-go"

# 复制编译好的程序
COPY --from=builder /src/metatube-server .

# 安装系统依赖（和你原版一样）
RUN apk add --no-cache ca-certificates tzdata

# 👇 👇 👇 你的所有环境变量 全部保留
ENV GIN_MODE=release
ENV PORT=8080
ENV TOKEN=""
ENV DSN=""
ENV REQUEST_TIMEOUT=""
ENV DB_MAX_IDLE_CONNS=0
ENV DB_MAX_OPEN_CONNS=0
ENV DB_PREPARED_STMT=0
ENV DB_AUTO_MIGRATE=0

# 👇 👇 👇 端口 8080 保留
EXPOSE 8080

# 启动命令（和原版一样）
ENTRYPOINT ["/metatube-server"]