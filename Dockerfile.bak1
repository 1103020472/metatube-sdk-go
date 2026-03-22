# 用 GO 1.25 + 完整镜像（满足项目要求）
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY . /src

# 强制忽略 GO 版本检查，直接下载依赖
RUN GOTOOLCHAIN=auto go mod tidy

# 直接编译（不用 make，不踩任何坑）
RUN CGO_ENABLED=0 GOOS=linux go build -o metatube-server ./cmd/server

# 运行阶段（完全不变）
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