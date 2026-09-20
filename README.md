# sentence-api

下一阶段计划见 [SQLite 默认部署与后台导入开发计划](docs/next-development.md)。该计划尚未实现，以下说明仍对应当前发布版本。

默认部署只需 `DB_HOST`、`DB_NAME`、`DB_USER`、`DB_PASSWORD`；`DB_PORT`（默认 `3306`）和 `DB_TLS` 可选。旧 `MYSQL_DSN` 仍兼容，但不能与 `DB_*` 混用。`WEB_SECRET_KEY` 与 `SITE_CONTACT` 可省略：首次启动会在 `/var/lib/quotewisp` 自动生成并持久化密钥，联系方式可在后台设置。使用 Compose 绑定目录时按下文步骤初始化，迁移机器时须带上整个 `data` 目录。

纯 Go 的只读语句 API。公开读取从原子内存快照完成，MariaDB/MySQL 只用于迁移、导入和后台刷新。当前部署验收基线为 MariaDB 11.8；MySQL 8.4 是待单独验证的兼容目标。

构建开发二进制：

```bash
go build -o sentence-api ./cmd/api
```

```bash
export DB_HOST=db.example DB_NAME=sentence_api DB_USER=sa_test_write DB_PASSWORD=change_me
./sentence-api migrate up
./sentence-api import --file testdata/sentences.json
DATA_DIR=./data COOKIE_SECURE=false ./sentence-api
```

服务默认监听 `:8080`。主要端点为 `/api/v1`、`/api/v1/sentences/{uuid}`、`/api/v1/categories`、`/healthz`、`/readyz`、`/metrics` 和 `/version`。服务启动不会自动迁移或导入。

随机与 UUID 查询使用统一的语句结构：`uuid`、`content`、`category`、`source`、`author`、`length`；响应不公开数据库内部 `id`。精确数据集版本位于字符串形式的 `meta.dataset_version`。

公开 API 默认允许所有网站跨域访问（不携带 credentials），也可用 `CORS_ALLOWED_ORIGINS` 配置明确白名单。完整的参数、方法、响应、错误、CORS、reload 和健康检查规则见 [开发规格](docs/development-spec.md)；导入 JSON 字段、严格校验、去重、冲突与事务语义见 [导入格式](docs/import-format.md)。

开发检查：

```bash
go test ./...
go test -race ./...
go vet ./...
```

设置 `MYSQL_TEST_DSN` 后，集成测试会创建随机命名的独立数据库、执行正式迁移，并在独立清理上下文中删除该数据库。未设置时测试明确跳过。

## Web 前台/后台

同一二进制还提供 `web` 子命令，启动前台站点、投稿与管理员后台；沿用前面导出的四个 `DB_*` 变量。生产 HTTPS 不要设置 `COOKIE_SECURE=false`；本地 HTTP 可使用：

```bash
DATA_DIR=./data COOKIE_SECURE=false ./sentence-api
```

中文名称、英文名称、站点标语、公开地址、联系方式、备案等到后台「站点设置」修改；上述环境变量只在空库首次启动时写入种子。

创建首个管理员（密码仅通过 stdin，需 `--password-stdin`）：

```bash
./sentence-api web admin create --username admin --password-stdin <<< 'your-secure-password'
```

常用管理命令：`web admin list`、`web admin reset-password --username U --password-stdin`、`web admin enable|disable --username U`。详见 [web-spec.md](docs/web-spec.md)。

部署拓扑：一个 `quotewisp` 容器监听 `:8080`，同一进程提供 `/api/*` 和 Web 路由；反向代理的 `/api/*` 与 `/` 均转发到该端口。生产部署目录为 `/home/andan/deploy/quotewisp`，其中放置 `compose.yaml` 和 `.env`。

本地/CI 集成测试需设置 `MYSQL_TEST_DSN`（可创建 `sentence_api_test_%` 前缀的库）。Makefile 提供 `make smoke-web`（需合并服务已启动，默认 `BASE=http://127.0.0.1:8080`）。

Compose 部署：

```bash
cd /home/andan/deploy/quotewisp
mkdir -p data
chmod 700 data
sudo chown 65532:65532 data
docker compose config -q
docker compose up -d
```

云端部署使用 DockerHub 私有仓库（镜像为 Linux `amd64`）。先执行 `docker login` 完成认证，再在 Compose 目录执行 `docker compose pull` 和 `docker compose up -d`；更新版本时重新 pull 后使用 `docker compose up -d --force-recreate`。

目录权限设为 `700`，`.env` 设为 `600`。Go 进程只读取注入的环境变量，`.env` 由 Compose 的 `env_file` 注入，不会被 Go 自动读取。`.env` 中的值不要加包裹引号；密码中的 `$` 按 literal 保留。Compose 的 `env_file` raw 格式要求 Compose `>=2.30`。修改 `.env` 后使用 `docker compose up -d --force-recreate` 使环境变量生效，不要只执行 `docker restart`。只运行一个 app 服务，复用已存在的外部 `1panel-network`，不创建数据库或 Nginx。

部署、回滚和性能验证分别见 [operations.md](docs/operations.md)、[performance.md](docs/performance.md) 与 [acceptance.md](docs/acceptance.md)。
站点图标已内置为 `/favicon.ico`，整站反向代理无需额外配置。
