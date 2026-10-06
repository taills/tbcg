# syntax=docker/dockerfile:1
# 「雾港奇谭」运行镜像：用未修改的框架源码构建游戏服务、管理服务、构建 worker 与管理前端，
# 并按框架约定把示例内容放在 /app/games/harbor。构建上下文是仓库根目录（见 ../docker-compose.yml）。

FROM golang:1.26.4-bookworm AS go-build
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN for cmd in game-server admin-server admin-bootstrap content-build-worker; do \
      CGO_ENABLED=0 go build -trimpath -o /out/$cmd ./cmd/$cmd || exit 1; \
    done \
 && CGO_ENABLED=0 GOBIN=/out go install github.com/jackc/tern/v2@v2.4.3
# 示例自己的发布工具：只依赖标准库，通过管理 API 驱动发布流程。
WORKDIR /src/examples/harbor
COPY examples/harbor/go.mod examples/harbor/go.sum ./
COPY examples/harbor/cmd/ ./cmd/
RUN CGO_ENABLED=0 go build -trimpath -o /out/harbor-seed ./cmd/harbor-seed

FROM node:22-alpine AS admin-build
RUN corepack enable
WORKDIR /src/admin
COPY admin/package.json admin/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY contracts/ /src/contracts/
COPY admin/ ./
RUN pnpm build

# 复用 PostgreSQL 镜像只为 psql / pg_isready / openssl；数据库本身由 compose 中的 postgres 服务运行。
FROM postgres:18.3-bookworm
COPY --from=go-build /out/ /usr/local/bin/
COPY --from=admin-build /src/admin/dist/ /app/admin/dist/
COPY contracts/ /app/contracts/
COPY server/migrations/ /app/migrations/
# 管理前端的静态资源路由固定读取 games/demo/assets，按框架约定一并放入。
COPY games/demo/assets/ /app/games/demo/assets/
COPY examples/harbor/content/ /app/games/harbor/content/
COPY examples/harbor/assets/ /app/games/harbor/assets/
COPY --chmod=0755 examples/harbor/docker/scripts/ /app/docker/
RUN install -d -o postgres -g postgres -m 0700 /var/lib/tbcg/artifacts
WORKDIR /app
USER postgres
