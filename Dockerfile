# syntax=docker/dockerfile:1.7
# 多阶段构建：前端 → Go（内嵌前端）→ 精简运行时

FROM node:22-alpine AS web
ARG NPM_REGISTRY=
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml* ./
COPY web/pnpm-workspace.yaml ./
RUN corepack enable && corepack prepare pnpm@9 --activate && \
    if [ -n "${NPM_REGISTRY}" ]; then pnpm config set registry "${NPM_REGISTRY}"; fi && \
    pnpm install --frozen-lockfile --prefer-offline
COPY web/ ./
RUN pnpm build

FROM golang:1.23-alpine AS server
ARG GOPROXY=https://goproxy.cn,direct
ARG VERSION=dev
WORKDIR /app/server
ENV GOPROXY=${GOPROXY}
COPY server/go.mod server/go.sum* ./
RUN go mod download
COPY server/ ./
COPY --from=web /app/server/web ./web
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /app/degradation-check ./

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=server /app/degradation-check ./
ENV PORT=8088 \
    DATA_DIR=/data \
    TZ=Asia/Shanghai
VOLUME ["/data"]
EXPOSE 8088
ENTRYPOINT ["/app/degradation-check"]
