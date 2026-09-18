# 前台、投稿与后台规格

配套开发规格版本：1.3  
状态：草案，待评审  
修订日期：2026-09-18

本文件定义 `sentence-web` 进程：面向人的前台站点、公开投稿、管理员后台。它与只读 API `sentence-api` 并列部署，共享同一个数据库和 `internal/` 包，但**不修改读 API 的任何行为**。读 API、数据模型、版本协议以 [开发规格](development-spec.md) 为准；本文只写它未覆盖的部分。

## 1. 边界与原则

包含：

- 前台页面：首页、接口文档、投稿、数据说明与下载。
- 投稿接收、限流、防滥用。
- 管理员登录、会话、多管理员管理。
- 投稿审核（通过 / 拒绝 / 修正后通过）。
- 已发布语句的停用与恢复（用于下架请求）。
- 句子库导出与署名。
- 审核元数据的保留期清理。
- 独立二进制、镜像、迁移 `000002`、测试与文档。

不包含：

- 分类的创建、改名、排序、启停（继续由导入或外部程序按版本协议处理，列为后续项）。
- 修改已发布语句的内容、出处、作者。
- 用户注册、投稿人账号、投稿状态自助查询。
- 邮件、站内信等通知。
- JSON 形式的投稿接口。
- 前端框架、构建工具链。

设计原则：

1. **写权限只在 `sentence-web`。** 读 API 继续用只读账号，投稿与审核用具备写权限的账号。两者可以独立重启、回滚、扩缩。
2. **所有影响读结果的写入走 §6.3 版本协议。** 审核通过、停用、恢复都在事务里先锁版本行，末尾递增一次版本；读 API 通过现有轮询自动加载，不需要通知。
3. **投稿本身不递增版本。** 待审记录不进入快照，写它不影响读结果。
4. **不引入前端框架、JS 构建、Redis、ORM。** 页面用 `html/template` 服务端渲染，`embed` 进二进制；进程内限流；一个新依赖 `golang.org/x/crypto`（argon2id）。
5. **个人信息最小化。** 联系方式只在后台可见、只保留必要时长；公开的只有投稿人自选昵称。

## 2. 部署拓扑

同域不同路径。OpenResty 按前缀分流：

| 路径 | 上游 |
| --- | --- |
| `/api/` | `sentence-api:8080` |
| 其余全部 | `sentence-web:8081` |

以下路径在公网域名上直接拒绝（`403` 或 `404`），只允许内网访问：`/healthz`、`/readyz`、`/metrics`、`/version`、`/internal/`。两个进程各自都有这四个探针路径，容器编排通过内网端口访问，不经公网域名。

`/admin/` 建议在 OpenResty 层额外限制来源 IP。这是纵深防御，不替代应用层鉴权。

CORS 只在读 API 上有意义，前台同域不涉及。

## 3. 数据模型

新增迁移 `000002`。三张表全部 `utf8mb4`、InnoDB、UTC。

### 3.1 submissions

```sql
CREATE TABLE submissions (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    content        TEXT NOT NULL,
    category_id    BIGINT UNSIGNED NOT NULL,
    source         VARCHAR(255) NULL,
    author         VARCHAR(128) NULL,
    nickname       VARCHAR(32) NULL,
    contact        VARCHAR(255) NULL,
    client_ip      VARBINARY(16) NULL,
    content_sha256 BINARY(32) NOT NULL,
    status         TINYINT UNSIGNED NOT NULL DEFAULT 0,
    reject_reason  VARCHAR(255) NULL,
    reviewed_by    BIGINT UNSIGNED NULL,
    reviewed_at    DATETIME(6) NULL,
    sentence_id    BIGINT UNSIGNED NULL,
    created_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                   ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_submissions_sentence (sentence_id),
    KEY idx_submissions_status_created (status, created_at),
    KEY idx_submissions_pending_hash (status, content_sha256),
    KEY idx_submissions_reviewed (status, reviewed_at),
    CONSTRAINT fk_submissions_category FOREIGN KEY (category_id) REFERENCES categories(id),
    CONSTRAINT fk_submissions_reviewer FOREIGN KEY (reviewed_by) REFERENCES admin_users(id),
    CONSTRAINT fk_submissions_sentence FOREIGN KEY (sentence_id) REFERENCES sentences(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

状态：`0` 待审，`1` 已通过，`2` 已拒绝。

- `contact`、`client_ip` 入库时非空，进入保留期后清空为 `NULL`（见第 9 节）。
- `content_sha256` 是 `content` 原始字节的 SHA-256，用于待审队列内的重复检测。
- `sentence_id` 在通过时写入，指向新建的 `sentences` 行。
- `sentences` 表不改动。已发布语句仍是唯一的读取来源；投稿人昵称通过 `submissions.sentence_id` 关联，读 API 不知道它的存在。

### 3.2 admin_users

```sql
CREATE TABLE admin_users (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    username       VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
    password_hash  VARBINARY(255) NOT NULL,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    created_by     BIGINT UNSIGNED NULL,
    created_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                   ON UPDATE CURRENT_TIMESTAMP(6),
    last_login_at  DATETIME(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_admin_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

- 用户名匹配 `^[a-z0-9][a-z0-9_-]{2,31}$`，不区分大小写地拒绝重复（入库前统一小写）。
- 密码 8–128 个码点，argon2id（`t=3, m=64MiB, p=2`，随机 16 字节盐），哈希以 PHC 字符串形式存储，参数随哈希保存以便将来调整。
- 所有管理员权限相同，不做角色。`created_by` 仅用于追溯，CLI 创建时为 `NULL`。
- 停用管理员时删除其全部会话。不允许停用最后一个启用的管理员。

### 3.3 admin_sessions

```sql
CREATE TABLE admin_sessions (
    token_hash     BINARY(32) NOT NULL,
    admin_id       BIGINT UNSIGNED NOT NULL,
    csrf_token     BINARY(32) NOT NULL,
    created_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_seen_at   DATETIME(6) NOT NULL,
    expires_at     DATETIME(6) NOT NULL,
    PRIMARY KEY (token_hash),
    KEY idx_admin_sessions_admin (admin_id),
    KEY idx_admin_sessions_expires (expires_at),
    CONSTRAINT fk_admin_sessions_admin FOREIGN KEY (admin_id) REFERENCES admin_users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

- 会话令牌 32 随机字节，Cookie 中传输 base64url 原文，数据库只存 SHA-256。
- 绝对有效期 24 小时，空闲 2 小时。`last_seen_at` 每次请求最多每 5 分钟写一次，避免每个请求写库。
- 会话表在后台，是为了停用管理员时能立即使其登出；签名 Cookie 做不到这一点。

### 3.4 schema 版本

迁移后 `schema_migrations.version = 2`。`sentence-api` 和 `sentence-web` 的 `CheckSchema` 同时接受版本 2；1.3 起 `sentence-api` 不再接受版本 1。

**回滚约束**：1.2 的 `sentence-api` 镜像只接受版本 1，迁移到 2 之后它无法启动。发布顺序必须是：先部署接受版本 2 的 `sentence-api` 1.3，再执行迁移 `000002`，再部署 `sentence-web`。`000002.down.sql` 删除三张新表；执行前确认待审队列可以丢弃。

## 4. 前台页面

所有页面 `html/template` 渲染，模板与静态资源（一份 CSS、极少量原生 JS）通过 `embed` 打进二进制。不依赖 CDN。页面语言中文。

| 路径 | 方法 | 内容 |
| --- | --- | --- |
| `/` | GET | 项目介绍；"随机一句"区块（浏览器 JS 同源调用 `/api/v1/sentences/random`，无 JS 时显示静态说明）；最近通过投稿前 20 条（内容、分类、昵称）；入口链接 |
| `/docs` | GET | 接口文档：端点、参数、响应结构、错误格式与 `code` 表、`curl` 和 `fetch` 示例、限制说明。当前启用分类表由服务端从数据库读取渲染，其余内容静态 |
| `/submit` | GET | 投稿表单 |
| `/submit` | POST | 提交投稿；成功 `303` 到 `/submit/done`，失败重新渲染表单并标注字段错误 |
| `/submit/done` | GET | 感谢页，说明审核流程和预期时间 |
| `/dataset` | GET | 数据说明：来源署名、许可、投稿数据的公开条件、下载链接、下架请求联系方式 |
| `/dataset/sentences.json` | GET | 当前全部已发布语句导出（见第 8 节） |
| `/dataset/LICENSE.txt` | GET | AGPL v3 全文 |

"随机一句"用浏览器调用而不是服务端查库，原因是这同时演示了接口，且不让 `sentence-web` 承担读流量。

"最近通过"来自 `submissions status=1 JOIN sentences`，按 `reviewed_at DESC`，进程内缓存 60 秒。只展示 `sentences.status = 1` 的记录，被停用的自动消失。

所有前台页面返回 `Cache-Control: no-store`（`/dataset/*` 例外，见第 8 节）。`GET` 路径同时接受 `HEAD`；`OPTIONS` 返回 `204` 和 `Allow`。未知路径 `404`，已知路径错误方法 `405`。错误页面是 HTML；`Accept: application/json` 时返回开发规格第 10 节格式的 `application/problem+json`。

## 5. 投稿

### 5.1 字段

| 字段 | 必填 | 约束 |
| --- | --- | --- |
| `content` | 是 | 1–1000 个码点，非空且不能全为空白，有效 UTF-8，不 trim、不归一化 |
| `category` | 是 | 当前启用分类的 `code` |
| `source` | 出处与作者至少一项 | 最多 255 个码点、1020 字节 |
| `author` | 出处与作者至少一项 | 最多 128 个码点、512 字节 |
| `nickname` | 否 | 1–32 个码点，公开展示；省略则前台显示"匿名" |
| `contact` | 是 | 3–255 个码点，任意文本（邮箱、QQ、微信等），不校验格式；仅后台可见 |

内容上限 1000 码点与随机接口的可查询范围一致。更长的语句可以导入，但不接受投稿，这是滥用面的取舍而不是数据模型限制。

`source`、`author` 空字符串统一存 `NULL`，与导入格式一致。

### 5.2 防滥用

按顺序检查，任一失败即拒绝，不落库：

1. **表单令牌**：`GET /submit` 签发 HMAC-SHA256 令牌（`WEB_SECRET_KEY` 签名，含签发时间和随机数），`POST` 必须携带。签发后 5 秒内或 2 小时后提交视为无效。
2. **蜜罐字段**：表单含一个 CSS 隐藏、名字看似正常的字段；非空即拒绝，返回与成功相同的感谢页，不落库。
3. **每 IP 限流**：滑动窗口，默认每小时 5 条、每天 20 条。真实 IP 用开发规格第 11.2 节相同规则解析，共用 `TRUSTED_PROXY_CIDRS`。
4. **待审队列上限**：`status = 0` 总数达到 `SUBMISSION_PENDING_LIMIT`（默认 1000）时拒绝新投稿，页面说明"审核队列已满"。计数进程内缓存 30 秒。
5. **待审重复**：同分类且 `content_sha256` 相同的待审记录已存在时拒绝，提示"相同内容已在审核中"。

限流状态只在进程内存，单实例部署足够。多实例时各实例独立计数，实际上限随实例数放大；届时改 OpenResty 限流，不引入共享存储。

限流触发返回 `429`，页面说明稍后再试，不透露具体阈值。

### 5.3 条款

表单旁固定展示，并有一个必须勾选的确认框：

- 投稿内容将在审核通过后公开，并作为句子库的一部分以与本站句子库相同的条件（第 8 节）开放下载。
- 昵称随内容公开；联系方式仅用于审核沟通，不公开，审核结束 90 天后删除。
- 投稿人确认拥有提交内容的权利，或内容属于可公开引用的范围。

确认框未勾选的提交按字段错误处理。

### 5.4 落库

单条 `INSERT`，不锁版本行，不递增版本。`client_ip` 存 16 字节（IPv4 映射到 IPv6）。

## 6. 管理员鉴权

### 6.1 登录

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/login` | GET | 登录表单 |
| `/admin/login` | POST | 校验用户名密码；成功创建会话，`303` 到 `/admin/` |
| `/admin/logout` | POST | 删除当前会话，清 Cookie，`303` 到 `/admin/login` |

- 用户名不存在与密码错误返回相同错误文案和相近响应时间；被停用账号同样按凭证错误处理。
- 登录限速：每 IP 15 分钟 10 次失败，每用户名 15 分钟 5 次失败；超限返回 `429`，不透露哪一项触发。
- 登录成功重置该用户名的失败计数，并更新 `last_login_at`。

### 6.2 会话 Cookie

`Set-Cookie: admin_session=<token>; Path=/admin; HttpOnly; SameSite=Strict; Secure`

`Secure` 由 `COOKIE_SECURE` 控制，默认 `true`；只在纯 HTTP 的本地开发时关闭。`Path=/admin` 使 Cookie 不随前台请求发送。

### 6.3 CSRF

所有 `/admin/` 下的 `POST`：

1. `Sec-Fetch-Site` 存在且不是 `same-origin` 或 `none` → 拒绝。
2. 表单必须携带隐藏字段 `csrf_token`，与会话表中的值恒定时间比较。

两层都要过。`SameSite=Strict` 是第三层。

### 6.4 未登录访问

`/admin/` 下除登录页外的任何路径，未登录或会话过期时 `303` 到 `/admin/login`，不带回跳参数（避免开放重定向）。

## 7. 后台功能

所有页面服务端渲染，操作全部是表单 `POST`，成功后 `303` 回列表页。

### 7.1 审核

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/` | GET | 待审列表，`created_at ASC`，每页 50，显示内容、分类、出处、作者、昵称、提交时间、待审重复标记 |
| `/admin/submissions?status=approved\|rejected` | GET | 已处理列表，`reviewed_at DESC`，每页 50 |
| `/admin/submissions/{id}` | GET | 详情：全部字段（含联系方式、IP）、审核人、审核时间；待审状态时可编辑 |
| `/admin/submissions/{id}/approve` | POST | 通过；表单可携带修正后的 `content`、`category`、`source`、`author`，修正覆盖原投稿字段后再发布 |
| `/admin/submissions/{id}/reject` | POST | 拒绝；`reject_reason` 1–255 码点，必填 |

通过事务：

1. `SELECT version FROM dataset_versions WHERE id = 1 FOR UPDATE`。
2. `SELECT ... FROM submissions WHERE id = ? FOR UPDATE`，状态必须为 `0`，否则回滚并提示"已被其他管理员处理"。
3. 校验（修正后的）字段满足开发规格第 6.2 节全部规则；分类必须启用。
4. 精确重复检查：`sentences` 中存在同 `category_id` 且 `content` 字节相同的已发布记录 → 回滚并提示"句库中已有相同语句"，管理员改为拒绝。该查询无索引，审核是低频操作，可以接受全表扫描；数据量增长到影响体验时再加哈希列。
5. 服务端生成 UUID v4，`length = utf8.RuneCountInString(content)`，`INSERT INTO sentences (..., status = 1, published_at = now)`。
6. `UPDATE submissions SET status = 1, reviewed_by, reviewed_at, sentence_id, content/category_id/source/author（若修正）`。
7. 版本 `+1`，受影响行数必须为 1。
8. `COMMIT`。

拒绝只更新 `submissions`，不锁版本行，不递增版本。

不提供批量操作。审核是逐条判断，批量按钮会诱导不看内容直接过。

### 7.2 已发布语句管理

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/sentences?uuid=` | GET | 按 UUID 查单条，显示全部字段、状态、关联投稿（若有） |
| `/admin/sentences/{uuid}/disable` | POST | `status 1 → 3`，锁版本行，版本 `+1` |
| `/admin/sentences/{uuid}/enable` | POST | `status 3 → 1`，锁版本行，版本 `+1` |

这是处理下架请求的最小工具。不提供编辑内容、删除记录、按内容搜索。

### 7.3 管理员管理

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/users` | GET | 列表：用户名、状态、创建时间、最近登录 |
| `/admin/users` | POST | 新建：用户名 + 初始密码（由创建者输入，两次确认） |
| `/admin/users/{id}/disable` | POST | 停用并删除其会话；拒绝停用自己和最后一个启用账号 |
| `/admin/users/{id}/enable` | POST | 启用 |
| `/admin/users/{id}/reset-password` | POST | 重置他人密码并删除其会话 |
| `/admin/password` | GET, POST | 修改自己密码，需输入当前密码；成功后删除自己其他会话 |

### 7.4 CLI

```text
sentence-web admin create --username <name>
sentence-web admin disable --username <name>
sentence-web admin reset-password --username <name>
```

密码从标准输入读取（不回显、两次确认），不接受命令行参数，不打印。`create` 用于首个管理员，后续应通过后台创建以留下 `created_by`。

## 8. 句子库导出与署名

### 8.1 导出

`GET /dataset/sentences.json` 返回当前全部已发布语句和全部启用分类，**格式与 [导入格式](import-format.md) 完全一致**，可以被 `sentence-api import` 原样导入。不含 `id`、`length`、`status`、时间字段、投稿人信息。

- 进程按 `SNAPSHOT_POLL_INTERVAL` 轮询 `dataset_versions.version`，变化时在一致性只读事务中重新生成导出内容并缓存在内存。
- 响应头：`Content-Type: application/json; charset=utf-8`、`Content-Disposition: attachment; filename="sentences-<version>.json"`、`ETag: "<version>"`、`Cache-Control: public, max-age=3600`。支持 `If-None-Match` 返回 `304`。
- 生成失败时保留上一版本缓存并记录错误；进程启动时必须生成成功才 ready。

### 8.2 署名与许可声明

`/dataset` 页面和所有前台页脚固定展示：

- 初始句子库来源：`hitokoto-osc/sentences-bundle`（链接），许可 AGPL v3。
- 本站句子库（含后续用户投稿）在相同条件下开放，下载链接。
- 程序为独立实现，许可另行说明（链接到仓库）。
- 下架请求联系方式（由 `SITE_CONTACT` 配置）。

这些文本是模板常量，不放数据库。

## 9. 保留期清理

每日一次（进程启动后 5 分钟首次运行，之后每 24 小时），对 `reviewed_at < now - SUBMISSION_RETENTION`（默认 `2160h` 即 90 天）的记录：

- `status = 2`：删除整行。
- `status = 1`：`UPDATE SET contact = NULL, client_ip = NULL`，昵称和其余字段保留。

同时删除 `admin_sessions` 中 `expires_at < now` 的行。

每次运行分批处理（每批 500 行），记录删除与清空计数。待审记录（`status = 0`）永不自动删除。

## 10. 配置

`sentence-web` 环境变量。与开发规格第 12 节同名者语义相同。

| 环境变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `HTTP_ADDR` | 否 | `:8081` | 监听地址 |
| `MYSQL_DSN` | 是 | 无 | 具备写权限的账号 |
| `MYSQL_MAX_OPEN_CONNS` | 否 | `10` | |
| `MYSQL_MAX_IDLE_CONNS` | 否 | `5` | |
| `MYSQL_CONN_MAX_LIFETIME` | 否 | `30m` | |
| `WEB_SECRET_KEY` | 是 | 无 | 32–256 字节可打印 ASCII，签名表单令牌；泄露只影响防滥用，不影响会话 |
| `COOKIE_SECURE` | 否 | `true` | 会话 Cookie 是否带 `Secure` |
| `SITE_CONTACT` | 是 | 无 | 下架请求联系方式，前台展示 |
| `SUBMISSION_RATE_PER_HOUR` | 否 | `5` | 每 IP 每小时 |
| `SUBMISSION_RATE_PER_DAY` | 否 | `20` | 每 IP 每天 |
| `SUBMISSION_PENDING_LIMIT` | 否 | `1000` | 待审队列上限 |
| `SUBMISSION_RETENTION` | 否 | `2160h` | 审核元数据保留期 |
| `SNAPSHOT_POLL_INTERVAL` | 否 | `60s` | 导出缓存的版本轮询间隔 |
| `TRUSTED_PROXY_CIDRS` | 否 | 空 | 与读 API 相同规则 |
| `LOG_LEVEL` | 否 | `info` | |
| `SHUTDOWN_TIMEOUT` | 否 | `10s` | |

数据库账号权限：`SELECT, INSERT, UPDATE, DELETE` 于 `sentence_api.*`，不需要 DDL。

## 11. 中间件、日志与指标

复用开发规格第 11 节的请求 ID、真实 IP、访问日志、panic 恢复、安全响应头中间件。实现时可将这些从 `internal/httpapi` 提取到共享包，读 API 行为不变。

额外安全头：`Content-Security-Policy: default-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'`。前台 JS 全部来自本进程，不需要放宽。`Referrer-Policy: same-origin`。

日志脱敏在第 11.4 节之外追加：不记录投稿内容、联系方式、昵称、密码、会话令牌、CSRF 令牌、表单令牌。审核操作记录 `admin_id`、`submission_id`、动作、结果，不记录内容。

指标（Prometheus，低基数）：

- `http_*`：与读 API 相同的请求指标。
- `web_submissions_total{result}`：`accepted`、`invalid`、`rate_limited`、`queue_full`、`duplicate`、`honeypot`。
- `web_reviews_total{action,result}`：`approve`/`reject` × `success`/`conflict`/`duplicate`/`error`。
- `web_sentence_status_changes_total{action}`：`disable`/`enable`。
- `web_login_attempts_total{result}`：`success`/`failure`/`rate_limited`。
- `web_pending_submissions`：待审数量（缓存值）。
- `web_retention_rows_total{action}`：`deleted`/`redacted`。
- `web_dataset_export_version`、`web_dataset_export_bytes`。

## 12. 启停

启动顺序：加载配置 → 连接数据库 → `CheckSchema` 版本 2 → 生成导出缓存 → 启动 HTTP → 启动版本轮询与清理任务。导出生成失败则启动失败。

`/readyz`：数据库最近一次探测成功且导出缓存存在，且未进入退出流程。

优雅退出与开发规格第 14.1 节一致：标记退出、停止接收、等待在途请求与后台任务、关闭连接池、共同期限。

## 13. 镜像与发布

新增 `Dockerfile.web`，与现有 Dockerfile 相同的多阶段、`scratch`、非 root、`CGO_ENABLED=0`、构建参数注入版本要求。模板和静态资源通过 `embed` 进入二进制，运行阶段不复制任何文件目录。镜像 `sentence-web:<tag>`、`sentence-web:git-<sha>`。

两个镜像使用同一个 Git 标签发布。CI 的 `image` 与 `publish` 任务同时构建两者。

## 14. 测试

- 单元：字段校验、限流窗口、表单令牌签发与校验、密码哈希与校验、CSRF 检查、Cookie 属性、模板渲染不 panic、错误页格式协商。
- fuzz：投稿表单解析、UUID 路径参数。
- 集成（`MYSQL_TEST_DSN`）：通过事务的版本递增与回滚、并发通过同一投稿只成功一次、重复检查、停用/恢复的版本递增、停用最后一个管理员被拒绝、保留期清理、导出可被 `sentence-api import` 原样导入且 `changed=false`。
- 竞态：`go test -race ./...` 覆盖限流器与缓存。
- 端到端 smoke：脚本创建管理员、投稿、登录、通过，然后验证读 API 在下一次轮询后返回该语句。

## 15. 待后续决定

- 分类管理界面。
- JSON 投稿接口（供第三方客户端）。
- 多实例时的共享限流。
- 投稿人自助查询状态。
