# 前台与管理后台

前台、后台与 API 共用一个进程和 SQLite 数据库，默认监听 `:8080`。启动及管理员初始化见 [README](../README.md)。

## 前台页面

所有页面 `html/template` 渲染，静态资源通过 `embed` 打进二进制。不依赖 CDN、不引入 JS 框架或构建工具链。前台使用 `site.css`；后台使用独立 `admin.css` 与侧栏布局，不套用前台导航和页脚。页面语言中文。

| 路径 | 方法 | 内容 |
| --- | --- | --- |
| `/` | GET | 介绍居中无标题、无入口按钮；随机一句，换一句居中；紧接下三条说明卡横排。页脚贴视口底部 |
| `/docs` | GET | 接口文档：先给四条分开说明的 `fetch` 示例（随机、按分类、限制长度、列出分类），再写端点、参数、响应结构；只说明参数错误 400、无匹配 404，不列完整错误 `code` 表。当前启用分类表来自公开数据缓存（“公开数据缓存”一节），其余内容静态 |
| `/submit` | GET | 投稿表单（双列表单）；底部最近通过投稿前 20 条（内容、分类、昵称） |
| `/submit` | POST | 提交投稿；成功 `303` 到 `/submit/done`，失败重新渲染表单并标注字段错误 |
| `/submit/done` | GET | 感谢页，说明审核流程和预期时间 |
| `/dataset` | GET | 数据说明：来源署名、许可、投稿数据的公开条件、下载链接、下架请求联系方式 |
| `/dataset/sentences.json` | GET | 当前全部已发布语句导出（见“句子库导出与署名”一节） |
| `/dataset/LICENSE.txt` | GET | AGPL v3 全文 |

"随机一句"用浏览器调用而不是服务端查库，原因是这同时演示了接口，且不让 `sentence-web` 承担读流量。

### 公开数据缓存

前台页面**不直接查询数据库**。`sentence-web` 持有一份进程内公开数据缓存，前台请求只读它：

- 启用分类列表（`code`、`name`、`sort_order`、语句数）。
- 最近通过的投稿 20 条（内容、分类、昵称），来自 `submissions status=1 JOIN sentences WHERE sentences.status=1`，按 `reviewed_at DESC`。
- 句子库导出 JSON（见“句子库导出与署名”）。
- 对应的 `dataset_version`。

缓存在同一个一致性事务中一次性生成，原子切换，与读 API 的快照语义一致。生成失败保留上一版本并记录错误。

刷新时机：

1. 启动时加载，失败则启动失败。
2. 本进程任何递增版本的事务提交成功后，立即在后台重新生成。写入者是自己，不需要等待轮询。
3. 每 `SNAPSHOT_POLL_INTERVAL` 轮询 `dataset_versions.version`，与缓存版本不同时重新生成，用于捕获外部导入。
4. 审核通过或拒绝不改变版本时，"最近通过"仍可能变化（通过会递增版本，拒绝不影响该列表），因此第 2 条覆盖了所有本进程引起的变化。

同一时刻最多一个生成任务在跑；重复触发合并为一次。刷新使用服务生命周期 context，并加入退出等待组；收到退出信号后取消进行中的生成，等它结束后再关闭数据库。

数据库不可用时前台照常显示缓存内容，只有需要写库的操作（投稿、后台）失败。

必须走数据库、不能用缓存代替的读取：投稿写入前的待审队列计数和待审重复检查（要看最新状态）、后台全部页面（管理员看的是数据库当前状态而不是副本）。

所有前台页面返回 `Cache-Control: no-store`（`/dataset/*` 例外，见“句子库导出与署名”一节）。`GET` 路径同时接受 `HEAD`；`OPTIONS` 返回 `204` 和 `Allow`。未知路径 `404`，已知路径错误方法 `405`。错误页面是 HTML；`Accept: application/json` 时返回[API 错误格式](development-spec.md#错误格式)格式的 `application/problem+json`。

## 投稿

### 字段

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

### 防滥用

按顺序检查，任一失败即拒绝，不落库：

1. **表单令牌**：`GET /submit` 签发 HMAC-SHA256 令牌（`WEB_SECRET_KEY` 签名，含签发时间和随机数），`POST` 必须携带。签发后 5 秒内或 2 小时后提交视为无效。
2. **蜜罐字段**：表单含一个 CSS 隐藏、名字看似正常的字段；非空即拒绝，返回与成功相同的感谢页，不落库。
3. **每 IP 限流**：滑动窗口，默认每小时 5 条、每天 20 条。真实 IP 用[可信代理解析](development-spec.md#可信代理解析)中的相同规则解析，共用 `TRUSTED_PROXY_CIDRS`。
4. **待审队列上限**：`status = 0` 总数达到 `SUBMISSION_PENDING_LIMIT`（默认 1000）时拒绝新投稿，页面说明"审核队列已满"。计数实时查询（`status` 有索引），不用缓存，避免缓存期内被灌超。
5. **待审重复**：同分类且 `content_sha256` 相同的待审记录已存在时拒绝，提示"相同内容已在审核中"。

限流状态只在进程内存，单实例部署足够。多实例时各实例独立计数，实际上限随实例数放大；届时改 OpenResty 限流，不引入共享存储。

限流触发返回 `429`，页面说明稍后再试，不透露具体阈值。

### 条款

表单旁固定展示，并有一个必须勾选的确认框：

- 投稿内容将在审核通过后公开，并作为句子库的一部分以与本站句子库相同的条件（见“句子库导出与署名”）开放下载。
- 昵称随内容公开；联系方式仅用于审核沟通，不公开，审核结束 90 天后删除。
- 投稿人确认拥有提交内容的权利，或内容属于可公开引用的范围。

确认框未勾选的提交按字段错误处理。

### 落库

单条 `INSERT`，不递增数据集版本。`client_ip` 存 16 字节（IPv4 映射到 IPv6）。

## 管理员鉴权

### 登录

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/login` | GET | 登录表单 |
| `/admin/login` | POST | 校验用户名密码；成功创建会话，`303` 到 `/admin/`（概况） |
| `/admin/logout` | POST | 删除当前会话，清 Cookie，`303` 到 `/admin/login` |

- 用户名不存在与密码错误返回相同错误文案和相近响应时间；被停用账号同样按凭证错误处理。
- 登录限速：每 IP 15 分钟 10 次失败，每用户名 15 分钟 5 次失败；进入密码校验前预占额度，进行中的尝试计入窗口。进程内同时进行的 Argon2 校验最多 2 个。超限返回 `429`，不透露哪一项触发。
- 校验通过后，在 `BEGIN IMMEDIATE` 写事务中读取账号，复核密码哈希与启用状态后再插入会话。哈希已变或账号已停用则按凭证错误处理。该写事务与重置密码事务串行化，避免旧密码在重置完成后仍能建会话。
- 登录成功重置该用户名的失败计数，并更新 `last_login_at`。

### 会话 Cookie

`Set-Cookie: admin_session=<token>; Path=/admin; HttpOnly; SameSite=Strict; Secure`

`Secure` 由 `COOKIE_SECURE` 控制，默认 `true`；只在纯 HTTP 的本地开发时关闭。`Path=/admin` 使 Cookie 不随前台请求发送。

### CSRF

所有 `/admin/` 下的 `POST`：

1. `Sec-Fetch-Site` 存在且不是 `same-origin` 或 `none` → 拒绝。
2. 表单必须携带隐藏字段 `csrf_token`，与会话表中的值恒定时间比较。

两层都要过。`SameSite=Strict` 是第三层。

### 未登录访问

`/admin/` 下除登录页外的任何路径，未登录或会话过期时 `303` 到 `/admin/login`，不带回跳参数（避免开放重定向）。

## 后台功能

所有页面服务端渲染，操作全部是表单 `POST`，成功后 `303` 回列表页。已登录页为侧栏 + 顶栏的后台壳（`admin.css`），登录页为独立卡片，均不出现前台导航与 AGPL 页脚。

### 概况

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/` | GET | 后台首页。展示已发布/停用句子数、启用分类、待审与已处理投稿、近 24 小时投稿、数据集版本。接口调用次数使用合并进程内统计。 |

登录成功 `303` 到本页。

### 审核

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/` | GET | 概况（见“概况”一节） |
| `/admin/submissions` | GET | 待审列表，`created_at ASC`，每页 50，显示内容、分类、出处、作者、昵称、提交时间、待审重复标记 |
| `/admin/submissions?status=approved\|rejected` | GET | 已处理列表，`reviewed_at DESC`，每页 50 |
| `/admin/submissions/{id}` | GET | 详情：全部字段（含联系方式、IP）、审核人、审核时间；待审状态时可编辑 |
| `/admin/submissions/{id}/approve` | POST | 通过；表单可携带修正后的 `content`、`category`、`source`、`author`，修正覆盖原投稿字段后再发布 |
| `/admin/submissions/{id}/reject` | POST | 拒绝；`reject_reason` 1–255 码点，必填 |

通过事务：

1. 开启 `BEGIN IMMEDIATE` 写事务，读取 `dataset_versions.id = 1` 的有效版本。
2. `SELECT ... FROM submissions WHERE id = ?`，状态必须为 `0`，否则回滚并提示"已被其他管理员处理"。
3. 校验（修正后的）字段满足[导入格式](import-format.md)的数据约束；分类必须启用。
4. 精确重复检查：`sentences` 中存在同 `category_id` 且 `content` 字节相同的已发布记录 → 回滚并提示"句库中已有相同语句"，管理员改为拒绝。该查询无索引，审核是低频操作，可以接受全表扫描；数据量增长到影响体验时再加哈希列。
5. 服务端生成 UUID v4，`length = utf8.RuneCountInString(content)`，`INSERT INTO sentences (..., status = 1, published_at = now)`。
6. `UPDATE submissions SET status = 1, reviewed_by, reviewed_at, sentence_id, content/category_id/source/author（若修正）`。
7. 版本 `+1`，受影响行数必须为 1。
8. `COMMIT`。

拒绝只更新 `submissions`，不递增数据集版本。

不提供批量操作。审核是逐条判断，批量按钮会诱导不看内容直接过。

### 句子管理

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/sentences` | GET | 列表，`id DESC`，每页 50。筛选：`category`（代码）、`status`（`1`/`3`）、`q`（内容关键词）、`uuid`（精确）。显示 UUID、内容摘要、分类、出处、作者、长度、状态、发布时间 |
| `/admin/sentences/new` | GET | 新增表单 |
| `/admin/sentences` | POST | 新增；成功 `303` 到详情页 |
| `/admin/sentences/{uuid}` | GET | 详情：全部字段、状态、关联投稿（若来自投稿，含昵称，不含联系方式）；可编辑 |
| `/admin/sentences/{uuid}` | POST | 编辑内容、分类、出处、作者 |
| `/admin/sentences/{uuid}/disable` | POST | `status 1 → 3` |
| `/admin/sentences/{uuid}/enable` | POST | `status 3 → 1` |

列表只显示 `status` 为 `1` 或 `3` 的记录；`0` 和 `2` 在当前数据模型中不由本进程产生。

关键词搜索用 `content LIKE ?` 且对 `%`、`_`、`\` 转义；关键词 1–64 码点。无索引，全表扫描；数据量增长到影响体验时再评估全文索引，不在首版引入。

新增：字段与投稿相同（无昵称、联系方式），但内容上限放宽到数据库容量而非 1000 码点。服务端生成 UUID v4，走“审核”一节通过事务的第 1、3、4、5、7、8 步，不涉及 `submissions`。

编辑事务：

1. 开启写事务并读取数据集版本。
2. `SELECT ... FROM sentences WHERE uuid = ?`。
3. 校验新字段满足[导入格式](import-format.md)的数据约束；目标分类必须启用；重算 `length`。
4. 若分类或内容变化，执行“审核”一节第 4 步的精确重复检查（排除自身）。
5. 四个业务字段与现值逐字节比较；**完全相同则回滚，不递增版本**，页面提示"无变化"。
6. `UPDATE sentences SET content, category_id, source, author, length`。UUID、`published_at`、`created_at` 不变。
7. 版本 `+1`，受影响行数必须为 1。`COMMIT`。

停用 / 恢复事务：开启写事务并读取版本 → 读取并检查当前状态是预期的源状态（否则提示已被处理）→ 更新状态 → 版本 `+1` → 提交。停用后该句从读 API 与导出中消失，但 UUID 保留，恢复后不变。

### 分类管理

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/categories` | GET | 列表，`sort_order ASC, code ASC`：代码、名称、排序、状态、已发布语句数、停用语句数 |
| `/admin/categories/new` | GET | 新建表单 |
| `/admin/categories` | POST | 新建：代码、名称、排序 |
| `/admin/categories/{code}` | GET | 详情与编辑表单 |
| `/admin/categories/{code}` | POST | 编辑名称、排序 |
| `/admin/categories/{code}/disable` | POST | 停用；表单必须携带 `confirm=<当前已发布语句数>`，与服务端事务内重新统计的数一致才执行 |
| `/admin/categories/{code}/enable` | POST | 启用 |

代码建后不可改。规则与[导入格式](import-format.md)中的分类约束一致：代码匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`，名称 1–64 码点、256 字节、不全为空白，排序为有符号 32 位整数。

所有写操作开启写事务并读取版本与目标记录、变更后版本 `+1`；名称与排序均无变化时回滚不递增版本。新建分类为启用状态，语句数为 0，读 API 的分类列表立即包含它。

停用分类使其下全部语句从读 API 与导出消失、随机接口对该分类返回 `400`；`confirm` 字段强制管理员看到影响条数。二次确认在页面上以"停用将影响 N 条语句"的形式呈现，`N` 由服务端渲染进隐藏字段。停用不改变语句自身的 `status`，恢复分类后语句原样回来。

### 管理员管理

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/users` | GET | 列表：用户名、状态、创建时间、最近登录 |
| `/admin/users` | POST | 新建：用户名 + 初始密码（由创建者输入，两次确认） |
| `/admin/users/{id}/disable` | POST | 停用并删除其会话；拒绝停用自己和最后一个启用账号 |
| `/admin/users/{id}/enable` | POST | 启用 |
| `/admin/users/{id}/reset-password` | GET, POST | 独立重置密码页；提交后更新目标账户密码并删除其现有会话 |
| `/admin/password` | GET, POST | 修改自己密码，需输入当前密码；成功后删除自己其他会话。更新事务复核验证时的密码哈希，若期间已被重置则拒绝 |

### 站点设置

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/settings` | GET | 表单：中文名称、英文名称、站点标语、站点公开地址、联系方式、源代码地址、备案号、备案链接 |
| `/admin/settings` | POST | 保存；成功 `303` 回本页。立即覆盖进程内前台展示，不递增数据集版本 |

所有管理员权限相同。校验失败以 `400` 重新渲染表单并提示原因。

### CLI

```text
sentence-api web                                   # 兼容入口，同进程提供 Web 与 API
sentence-api web admin create --username <name> --password-stdin
sentence-api web admin disable --username <name>
sentence-api web admin reset-password --username <name> --password-stdin
```

密码通过 `--password-stdin` 从标准输入读取，不作为命令行参数，也不打印。交互式隐藏输入由部署者的 shell 完成，CLI 不提供两次确认提示。`create` 仅用于首个管理员；数据库已有任何管理员（含停用账号）时拒绝，并发初始化也只能成功一次。后续通过后台创建以留下 `created_by`，恢复访问使用 `reset-password` 或 `enable`。`admin` 子命令使用 `DATA_DIR` 中的 SQLite，不要求其他 Web 配置。

## 句子库导出与署名

### 导出

`GET /dataset/sentences.json` 返回当前全部已发布语句和全部启用分类，**格式与 [导入格式](import-format.md) 完全一致**，可以被 `sentence-api import` 原样导入。不含 `id`、`length`、`status`、时间字段、投稿人信息。

- 导出内容是公开数据缓存（“公开数据缓存”一节）的一部分，随缓存一起生成和切换。
- 响应头：`Content-Type: application/json; charset=utf-8`、`Content-Disposition: attachment; filename="sentences-<version>.json"`、`ETag: "<version>"`、`Cache-Control: public, max-age=3600`。支持 `If-None-Match` 返回 `304`。
- 生成失败时保留上一版本缓存并记录错误；进程启动时必须生成成功才 ready。

### 署名与许可声明

来源声明只在 `/dataset`（模板常量，不入库）：

- 初始句子库来源：`hitokoto-osc/sentences-bundle`（链接），许可 AGPL v3。
- 本站句子库（含后续用户投稿）在相同条件下开放，下载链接。

前台页脚不重复这段。页脚一行：左版权与可选 GitHub 图标源码链接，右备案；下架联系方式只在 `/dataset`。所有外链 `target="_blank"`，并带 `rel="noopener noreferrer"`。

以下来自 `site_settings`（后台「站点设置」）：

- 中文名称（导航、页脚版权、后台品牌）、可选英文名称（组合标题与后台品牌）和可选站点标语（首页）。
- 程序源代码链接（空则不展示；页脚以 GitHub 图标显示）。
- 下架请求联系方式（仅 `/dataset`）。
- 备案号与可选备案链接（空则不展示）。
- `/docs` 示例中的 API 基址（`public_origin`；空则示例使用相对路径）。

## 保留期清理

每日一次（进程启动后 5 分钟首次运行，之后每 24 小时），对 `reviewed_at < now - SUBMISSION_RETENTION`（默认 `2160h` 即 90 天）的记录：

- `status = 2`：删除整行。
- `status = 1`：`UPDATE SET contact = NULL, client_ip = NULL`，昵称和其余字段保留。

同时删除 `admin_sessions` 中 `expires_at < now` 的行。

每次运行分批处理（每批 500 行），记录删除与清空计数。待审记录（`status = 0`）永不自动删除。

## 文件导入

`/admin/imports` 支持原生 JSON 与 Hitokoto JSON。上传后异步解析，核对预览后确认；导入结果区分数据提交与快照刷新。刷新失败可重试刷新，不重复写入。同一实例同时只处理一个导入任务。字段、大小限制和失败行为见 [导入格式](import-format.md)。

## 配置与测试

生产 HTTPS 使用 `COOKIE_SECURE=true`；本地 HTTP 可设为 `false`。`WEB_SECRET_KEY` 可省略，程序会在 `DATA_DIR` 生成并保存密钥。`SITE_CONTACT`、`SITE_REPO_URL` 可作为站点初始配置；后台设置持久化在数据库中。

执行 `go test -race ./...` 验证账号、会话、投稿、管理写入与刷新契约；`make e2e-web` 验证真实 HTTP 投稿、审核和公开 API 更新。备份必须同时保存数据库与密钥，见 [运行文档](operations.md)。
