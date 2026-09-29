# API 与开发

项目使用 Go，API、前台和管理后台在同一进程中运行。公开读取使用原子内存快照；SQLite 负责持久化与后台写入。数据库事务递增数据集版本后，刷新任务加载并校验新快照，成功后整体替换；失败时继续使用有效旧快照。空句库也是有效快照。

## 本地检查

```sh
go test ./...
go test -race ./...
go vet ./...
make e2e-web
```

数据库测试使用临时 SQLite 文件，不依赖外部服务。`make fuzz` 执行查询、UUID、错误响应和投稿校验的模糊测试。数据库结构与事务见 [SQLite 存储](sqlite-only.md)，Web 功能见 [前台与管理后台](web-spec.md)。

## HTTP API

业务接口统一前缀为 `/api/v1`。成功 JSON 使用 `application/json; charset=utf-8`，所有文本使用 UTF-8。

### 通用约定

- 对外 `dataset_version` 使用十进制字符串，避免 JavaScript 整数精度损失；内部数据库 `id` 不通过语句接口公开。`length`、分类 `count` 和错误 `status` 使用 JSON 数字。
- 所有服务可控响应包含 `X-Request-ID`。
- 公开读取路由允许 `GET`、`HEAD`、`OPTIONS`。`HEAD` 执行与 `GET` 相同的校验与状态判断，返回相应响应头，不返回响应体。
- `OPTIONS` 不执行随机查询，合法预检及普通能力查询返回 `204`；方法头和 CORS 行为见下文中间件与安全说明。业务查询参数校验适用于 `GET`、`HEAD`。
- 业务接口不接受请求体；已携带实体的公开读取请求返回 `400`。
- 已知路径上不支持的方法返回 `405`，包含准确的 `Allow`；未知路径返回 `404`。
- 拒绝畸形查询编码、重复参数名、未知参数名和超限查询。随机接口原始查询字符串最多 4096 字节，超限返回 `400`。
- UUID、分类、健康、版本、指标接口不接受查询参数。
- 根路径 `/` 不提供兼容接口。
- 随机、分类、UUID 及控制接口返回 `Cache-Control: no-store`，避免中间缓存导致随机性或版本可见性异常。

### 随机语句

```http
GET /api/v1
```

| 参数 | 类型 | 默认值 | 限制 |
| --- | --- | --- | --- |
| `categories` | 逗号分隔字符串 | 全部启用分类 | 去重后最多 20 个 |
| `min_length` | 十进制整数 | `0` | 0–1000 |
| `max_length` | 十进制整数 | `30` | 0–1000，必须大于等于最小值 |

分类规则：

- 参数省略表示全部启用分类；显式空字符串、空元素、连续逗号、空白及非法代码返回 `400`。
- 分类代码匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`，不 trim，不忽略大小写。
- 先去重，再检查 20 个分类上限；解析工作量由统一的 4096 字节查询长度限制约束，不再增加另一套原始项数限制。
- 20 个分类仅是单次显式筛选条件的接口限制，不限制句库分类总数；省略 `categories` 时查询全部启用分类。
- 未知分类或停用分类返回 `400`。存在但当前无语句的启用分类属于合法分类。
- 查询中任一分类非法即拒绝整次请求，不忽略非法分类。

长度规则：

- 只接受 `[0-9]+` 形式，允许前导零；不接受空字符串、符号、空白、小数、科学计数法和溢出整数。
- `0` 和 `1000` 均为合法边界；`min_length = max_length` 表示精确长度。
- 默认值独立应用。例如仅传 `min_length=50` 时，`max_length` 仍为 30，因此返回 `400`。
- 有效数据不含零长度语句，因此 `[0,0]` 是合法查询，但结果为 `404`。

成功示例：

```json
{
  "data": {
    "uuid": "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2",
    "content": "今天也要认真写代码。",
    "category": "f",
    "source": "项目自编示例",
    "author": "",
    "length": 10
  },
  "meta": {"dataset_version": "1289"}
}
```

没有符合条件的数据时返回 `404`，不得回退到其他长度或分类。

### 根据 UUID 查询

```http
GET /api/v1/sentences/{uuid}
```

- 按 [导入格式](import-format.md) 中的 UUID 规则校验并规范化。
- 格式错误返回 `400`。
- 不存在、未发布或所属分类未启用返回 `404`。
- 只查询当前快照的 `ByUUID`，不访问数据库。
- 成功响应与随机接口使用相同的数据结构及版本元数据。

### 分类列表

```http
GET /api/v1/categories
```

返回所有启用分类，包括零语句分类，按 `sort_order ASC, code ASC` 排列。

```json
{
  "data": [
    {"code": "f", "name": "原创", "count": 120},
    {"code": "poetry", "name": "诗歌", "count": 0}
  ],
  "meta": {"dataset_version": "1289"}
}
```

`count` 为当前快照该分类的全部语句数，不受随机接口默认长度 30 的影响。

### 健康检查

```http
GET /healthz
```

只确认进程和 HTTP 循环正常，不检查数据库。成功返回 `200`：

```json
{"status":"ok"}
```

```http
GET /readyz
```

存在有效快照且未进入退出流程时返回 `200`：

```json
{"status":"ready","dataset_version":"1289"}
```

数据库操作暂时失败但旧快照有效时仍保持 ready，通过指标报告数据库异常。没有快照或已经开始退出时返回 `503` 问题响应。退出是“有效快照即可 ready”的明确例外。

### 指标

```http
GET /metrics
```

返回 Prometheus 文本格式。包含请求、快照、刷新、数据库和 Go 运行时指标。

指标不是业务 JSON。部署方应限制抓取来源，不在公网无限制暴露。

### 版本信息

```http
GET /version
```

```json
{
  "version": "1.0.0",
  "git_commit": "0123456789abcdef",
  "build_time": "2026-09-18T00:00:00Z"
}
```

仅包含程序版本、Git 提交和构建时间，不包含主机名及环境变量。本地未注入构建信息时可使用 `dev`、`unknown`；发布镜像不得缺失这些信息。程序版本与开发规格版本分别管理。

## 错误格式

服务可控错误统一使用 `application/problem+json`，包括参数错误、未找到、错误方法、鉴权失败、刷新冲突、未就绪和可恢复 panic。

```json
{
  "type": "about:blank",
  "title": "参数无效",
  "status": 400,
  "detail": "max_length 必须大于等于 min_length",
  "request_id": "4eb245ba-2340-420a-8e8b-6f6034c1dcde",
  "code": "invalid-parameter"
}
```

首版使用 `about:blank`，增加稳定的 `code` 扩展字段供客户端识别；不使用不受项目控制的示例域名作为正式错误类型。

| HTTP 状态 | code | 场景 |
| --- | --- | --- |
| 400 | `invalid-parameter` | 参数、UUID、查询编码或不支持的请求实体 |
| 401 | `unauthorized` | 手动刷新凭证无效 |
| 403 | `cors-denied` | 明确被拒绝的 CORS 预检 |
| 404 | `not-found` | 未知路径、缺失语句或没有匹配数据 |
| 405 | `method-not-allowed` | 已知路径上方法不受支持 |
| 409 | `reload-in-progress` | 已有快照构建任务 |
| 500 | `internal-error` | 可恢复内部错误 |
| 503 | `not-ready` | 无有效快照或服务正在退出 |

要求：

- 客户端错误描述不得包含数据库原始错误、堆栈、DSN、内部路径、令牌或未经处理的输入全文。
- `HEAD` 错误只返回相应头和状态，不返回问题响应体。
- 在提交响应头前完成业务 JSON 序列化，尽量保证 panic 或编码错误能统一处理。
- 响应已经提交后发生错误，不得再拼接第二个 JSON 或声称可以改写状态码；记录内部错误并终止响应。
- `net/http` 在中间件执行前拒绝的畸形报文、超大请求头以及连接级超时，不承诺统一 JSON 格式和请求 ID。不得为统一格式自行重写 HTTP 协议栈。

## 中间件与安全

中间件顺序：

1. Request ID。
2. 真实客户端 IP 解析。
3. 访问日志与请求指标。
4. Panic recovery。
5. 安全响应头。
6. CORS。
7. 路由。

### HTTP 服务限制

- `ReadHeaderTimeout`：5 秒。
- `ReadTimeout`：10 秒。
- `WriteTimeout`：10 秒。
- `IdleTimeout`：60 秒。
- `MaxHeaderBytes`：1 MiB。
- 服务生成 UUID 请求 ID，忽略客户端传入的 `X-Request-ID`，在响应中返回新 ID。
- 至少设置 `X-Content-Type-Options: nosniff` 和 `X-Frame-Options: DENY`。HTTPS 专属策略由外部 TLS 终止层配置。
- 禁止 JSONP、动态 callback 和可执行 JavaScript。
- 公开读取路由仅允许 `GET`、`HEAD`、`OPTIONS`；内部 reload 仅允许 `POST`。
- 限流优先由外部 Nginx 完成。首版不要求 Go 内置限流；后续可增加单实例令牌桶，不得因此引入 Redis。

### 可信代理解析

- 默认使用 TCP 直接连接方地址。
- 仅当直接连接方属于 `TRUSTED_PROXY_CIDRS` 时解析 `X-Forwarded-For`。
- 首版仅支持 `X-Forwarded-For`，忽略 `Forwarded` 和 `X-Real-IP`，避免来源优先级歧义。
- 从直接连接方开始，自右向左检查完整代理链，剔除可信代理，取首个不可信地址为客户端 IP；全部可信时取链中最左地址。
- 头内只接受裸 IPv4 或 IPv6 地址，不接受端口、主机名、`unknown` 或带引号值；多行头按顺序拼接处理。
- 最多解析 32 个转发地址。格式错误、空元素或超限时回退为直接连接方，不采用部分解析结果。
- IPv4 映射 IPv6 地址先规范化再匹配 CIDR。
- 部署文档必须提示：边缘代理应覆盖或正确追加该头，可信网段只能包含实际受控代理。

### CORS

- 默认允许所有网站访问公开 API（等价于 `*`），无需配置白名单；CORS 不作为服务端鉴权措施，且不携带 credentials。
- `CORS_ALLOWED_ORIGINS` 可选，用于把公开 API 限制为明确的 HTTP/HTTPS origin 列表；不能包含路径、查询、片段、用户信息、`null` 或显式 `*`。未设置或为空时默认返回 `*`；显式白名单时仅匹配来源返回该 origin。
- 只对 `/api/v1`、`/api/v1/categories` 和 `/api/v1/sentences/{uuid}` 路由启用 CORS，内部 reload、健康、版本和指标接口不开放 CORS。
- 预检仅允许 `GET`、`HEAD` 和请求头 `Accept`、`Content-Type`，头名比较不区分大小写。不启用 credentials。
- 来源、请求方法或请求头不被允许的预检返回 `403`；预检只接受 `GET`、`HEAD` 以及 `Accept`、`Content-Type` 请求头。
- 普通、不带 CORS 预检头的 `OPTIONS` 返回 `204` 和 `Allow`。
- 正确设置 `Vary: Origin`；预检额外包含 `Access-Control-Request-Method` 和 `Access-Control-Request-Headers`，不能覆盖已有的 `Vary` 值。
- `Access-Control-Expose-Headers` 包含 `X-Request-ID`，使允许的前端可以读取请求 ID。

### 日志与凭证

- 管理令牌只能通过环境变量注入，不提供命令行令牌参数或持久化配置文件。
- 不记录完整 DSN、令牌、密码、Authorization、Cookie、完整查询字符串、语句内容或请求体。
- 配置、驱动和数据库错误在日志中使用经过审查的错误类别及必要错误码，不直接输出可能包含凭证的原始错误字符串。
- 不向客户端返回 panic 内容或堆栈；内部诊断日志也必须遵守凭证脱敏规则。

## 主要配置

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `DATA_DIR` | `/var/lib/quotewisp` | 数据库和密钥目录 |
| `HTTP_ADDR` | `:8080` | 合并服务监听地址 |
| `SNAPSHOT_POLL_INTERVAL` | `1m` | 数据集版本检查间隔 |
| `SNAPSHOT_LOAD_TIMEOUT` | `30s` | 快照加载期限 |
| `RELOAD_TOKEN` | 空 | 启用手动刷新接口的令牌 |
| `CORS_ALLOWED_ORIGINS` | 空 | 公开 API 的来源白名单；空时允许所有来源 |
| `TRUSTED_PROXY_CIDRS` | 空 | 可信代理网段 |
| `LOG_LEVEL` | `info` | 日志级别 |
| `SHUTDOWN_TIMEOUT` | `10s` | 优雅退出期限 |

配置按命令校验，格式错误明确失败。收到 SIGTERM/SIGINT 后退出就绪状态，取消后台任务并在退出期限内排空请求。部署与发布分别见 [运行文档](operations.md) 和 [发布说明](../.github/RELEASE.md)。
