# 编译阶段
FROM golang:alpine AS builder
WORKDIR /src
COPY . /src

RUN apk add --no-cache git

# 进入项目真正的代码目录
WORKDIR /src/cmd/server

# 编译（这里才有 main.go）
RUN CGO_ENABLED=0 GOOS=linux go build -o metatube-server .

# 运行阶段
FROM alpine:latest
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.source="https://github.com/1103020472/metatube-sdk-go"

# 从编译阶段复制程序
COPY --from=builder /src/cmd/server/metatube-server /

RUN apk add --no-cache ca-certificates tzdata

ENV GIN_MODE=release
ENV PORT=8080
ENV TOKEN=""
ENV DSN=""
ENV REQUEST_TIMEOUT=""
ENV DB_MAX_IDLE_CONNS=0
ENV DB_MAX_OPEN_CONNS=0
ENV DB_PREPARED_STMT=0
ENV DB_AUTO_MIGRATE=0

EXPOSE 8080

ENTRYPOINT ["/metatube-server"]