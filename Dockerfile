# 编译阶段
FROM golang:alpine AS builder
WORKDIR /src
COPY . /src

RUN apk add --no-cache git
RUN go mod tidy

# 关键修复：你的项目必须用 go build . 或者 make 编译
RUN CGO_ENABLED=0 GOOS=linux go build -o metatube-server .

# 运行阶段
FROM alpine:latest
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.source="https://github.com/1103020472/metatube-sdk-go"

COPY --from=builder /src/metatube-server .

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