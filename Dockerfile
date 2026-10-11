# 单镜像同时装路由器和注册机：router 需要拉起 farm_http.py，
# 拆成两个容器反而要额外处理进程通信和共享目录。
#
# 构建：docker build -t mistral-router .
# 运行：见 docker-compose.yml

# ── 构建阶段 ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /src
# 先拷依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY web ./web
# CGO_ENABLED=0 才能得到静态二进制，运行阶段用 alpine 也无需额外运行库
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mistral-router .

# ── 运行阶段 ──────────────────────────────────────────────────────────────
FROM python:3.12-slim

# tzdata 让日志时间可读；curl 供 healthcheck 用
RUN apt-get update \
 && apt-get install -y --no-install-recommends tzdata curl \
 && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai \
    PYTHONUNBUFFERED=1 \
    FARM_DIR=/app \
    DATA_DIR=/app/data \
    PORT=7623

WORKDIR /app

# 注册机的 Python 依赖
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

# 路由器二进制
COPY --from=builder /out/mistral-router /app/bin/mistral-router

# 注册机与配置
COPY http_farm ./http_farm
COPY farm_http.py ./
COPY config ./config
COPY Makefile ./
COPY scripts ./scripts

# 运行时目录（会被 compose 的卷覆盖）
RUN mkdir -p /app/data/logs /app/results

# 默认不携带任何密钥；通过环境变量或挂载 .env 注入
EXPOSE 7623

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:7623/healthz || exit 1

CMD ["/app/bin/mistral-router"]
