# 拾句 / Quotewisp（sentence-api）

纯 Go 的语句 API、投稿前台和管理后台，同一进程监听 `:8080`。公开 API 从原子内存快照读取；数据库用于持久化、后台管理和快照刷新。

默认使用 SQLite，无需配置外部数据库。首次启动自动迁移并写入少量自编示例，后续启动不会重复写入；删除全部语句后仍可正常启动。SQLite 数据库 `quotewisp.db` 和自动生成的 `web-secret.key` 均位于 `DATA_DIR`（默认 `/var/lib/quotewisp`）。已有 MySQL/MariaDB 配置继续可用，启动时也会自动迁移表。

## 本地运行

```bash
go build -o bin/sentence-api ./cmd/api
DATA_DIR=./data COOKIE_SECURE=false ./bin/sentence-api
```

另开终端创建管理员，密码仅通过标准输入提供：

```bash
read -rs -p '管理员密码: ' admin_password
printf '%s\n' "$admin_password" | DATA_DIR=./data ./bin/sentence-api web admin create --username admin --password-stdin
unset admin_password
```

访问 `http://localhost:8080`，后台位于 `/admin/`。生产 HTTPS 保持 `COOKIE_SECURE=true`；`false` 仅用于本地 HTTP。管理员没有默认密码，也没有公开安装向导。

## Docker Compose

从源码目录构建并启动（Compose >= 2.30）：

```bash
docker compose up -d --build
```

默认映射 `18080:8080`，使用命名卷 `quotewisp-data` 保存数据库及密钥，无需 `.env` 或外部网络。需要本地 HTTP 登录时，创建 `.env` 并写入 `COOKIE_SECURE=false` 后重建容器。生产应由 HTTPS 反向代理转发到容器端口。

```bash
read -rs -p '管理员密码: ' admin_password
printf '%s\n' "$admin_password" | docker compose exec -T quotewisp /sentence-api web admin create --username admin --password-stdin
unset admin_password
```

默认镜像名是 `quotewisp:local`，不会使用尚未包含 SQLite 功能的旧发布镜像。使用已发布且包含本次功能的镜像时，可设置 `QUOTEWISP_IMAGE`，拉取后执行 `docker compose up -d --no-build`。修改 `.env` 后执行 `docker compose up -d --force-recreate`。

也可将卷替换为 `./data:/var/lib/quotewisp`，但需提前创建目录并赋予 UID/GID `65532:65532` 写权限，目录权限设为 `700`。已有部署升级时保留原卷或绑定目录，勿直接用新命名卷替换原有密钥目录；外部数据库配置也须保留。

## 可选 MySQL / MariaDB

同时设置 `DB_HOST`、`DB_NAME`、`DB_USER`、`DB_PASSWORD` 即使用外部数据库。`DB_PORT` 默认 `3306`，`DB_TLS` 可选。旧 `MYSQL_DSN` 仍兼容，但不得与任何 `DB_*` 混用；部分配置或错误配置直接失败，不会回落 SQLite。

数据库及账号由部署方创建，应用自动迁移表，因此账号需要目标库的 DDL 权限。已有数据库升级不会混入示例数据，不会自动将 MariaDB 数据搬到 SQLite。MariaDB 11.8 是既有验收基线，MySQL 8.4 仍需独立兼容性验证。

## 管理与导入

后台支持语句、分类、投稿审核、管理员及站点设置。登录后进入 `/admin/imports`，可上传原生 JSON 或 Hitokoto JSON，查看预览后确认导入。任务异步执行，结果分别显示数据写入与快照刷新；刷新失败可单独重试。原生命令行导入继续可用：

```bash
DATA_DIR=./data ./bin/sentence-api import --file testdata/sentences.json --dry-run
DATA_DIR=./data ./bin/sentence-api import --file testdata/sentences.json
```

显式 `migrate up` 仍可用于部署预检，只创建或升级表；预先迁移的空库按已有库处理，不自动添加示例。常用管理员命令：`web admin list`、`web admin reset-password --username U --password-stdin`、`web admin enable|disable --username U`。

主要端点为 `/api/v1`、`/api/v1/sentences/{uuid}`、`/api/v1/categories`、`/healthz`、`/readyz`、`/metrics` 和 `/version`。空库就绪后 `/readyz` 返回 200，随机查询无匹配返回 404。随机与 UUID 查询返回 `uuid`、`content`、`category`、`source`、`author`、`length`，不公开内部 `id`；精确数据集版本位于字符串 `meta.dataset_version`。

公开 API 默认允许不携带 credentials 的跨域访问，可用 `CORS_ALLOWED_ORIGINS` 配置白名单。完整规则见 [开发规格](docs/development-spec.md)、[Web 规格](docs/web-spec.md) 和 [导入格式](docs/import-format.md)。

## 检查与备份

```bash
go test ./...
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/api
```

SQLite 集成测试不依赖外部服务。设置 `MYSQL_TEST_DSN` 后，MariaDB 集成测试会创建随机命名的独立数据库、执行正式迁移并清理；未设置时明确跳过。

SQLite 使用 WAL。备份时停止应用后复制整个数据目录（含数据库、WAL/SHM 和密钥），或使用 SQLite 一致性备份工具；禁止运行中只复制 `quotewisp.db`。恢复前停止应用，保留原文件并恢复目录权限。高于当前程序支持的数据库版本会拒绝启动，不会自动降级。详见 [运行与回滚](docs/operations.md)。

阶段目标与验收范围见 [下一阶段开发计划](docs/next-development.md)。`docs/deployment-*.md` 和单容器部署记录是历史验收记录，不代表本地改动已经发布或生产已经升级。
