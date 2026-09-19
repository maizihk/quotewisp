# 发布追溯准备与冻结清单

审计日期：2026-09-20（Asia/Shanghai）

本文是发布审核记录。当前本地候选的 API/Web、性能、故障、公开浏览器、后台安全、管理员账户闭环及站点设置隔离验收均有通过证据，适合进入本地冻结候选；这不表示已完成正式 clean release 或 registry 推送。此前 Web dirty 部署与回滚记录保留在 [`docs/deployment-20260920.md`](deployment-20260920.md)，API 未变。

## 已核实

- [x] `.github/RELEASE.md` 已规定：只有受保护的 `vMAJOR.MINOR.PATCH` 标签进入 `publish`；发布环境需要 reviewer；发布摘要记录不可变 digest。
- [x] 发布工作流要求 `quality`、`fuzz`、`integration`、`image` 全部通过，并把完整提交哈希、标签和 UTC 构建时间写入镜像。
- [x] `image` job 会构建 scratch 镜像，检查运行用户 `65532:65532`，执行 `web` 帮助检查和 Web smoke；MariaDB 11.8 只用于镜像 smoke，不替代外部 DSN 集成门禁。
- [x] `Dockerfile` 当前复制 `migrations/*.sql`；`.dockerignore` 排除 `.git`、`.github`、`.cache`、凭证文件、文档、脚本和构建输出；`.cache/` 由 `.dockerignore` 排除构建上下文，由 `.gitignore` 排除候选提交。
- [x] `docs/performance.md` 记录的 2026-09-20 正式隔离复测通过既定 API 性能阈值；`docs/fault-acceptance.md` 记录数据库中断、旧快照服务、恢复刷新和优雅退出均通过。两份证据只覆盖隔离实例，不能宣称全站上线通过。
- [x] 可复用脚本已确认：`scripts/smoke.sh`、`scripts/smoke-web.sh`、`scripts/e2e-web.sh`、`scripts/test-mariadb.sh`；运行时凭证应通过环境变量注入，禁止写入报告或镜像。

## 当前阻塞与待做

- [ ] 工作树待冻结：当前仍有大量已修改和未跟踪路径，包含业务代码、迁移、CI、Dockerfile、文档及 Web 交付文件。负责人需逐项审阅并形成单一发布提交；当前 dirty 状态不能打正式标签。
- [ ] `.github/RELEASE.md`、`.github/workflows/ci.yml`、`Dockerfile`、`Makefile` 均有未提交修改；这些发布控制文件必须纳入同一提交并重新跑门禁。
- [x] 已在部署 VM 上只读核对容器：`sentence-web-test` 使用 `sentence-api:release-fixes-20260919t190026z`、运行中、UID/GID `65532:65532`、入口 `/sentence-api`；`sentence-api-test` 使用 `sentence-api:overview-20260919t153429z`、运行中、同一 UID/GID 和入口。两者 `ReadonlyRootfs=false`，需在正式部署前决定是否启用只读根文件系统。只读 inspect 记录：release-fixes 本地 image ID `sha256:0cb1fd9412addf6a587c19ad42d31d79b9103a115f50c037527e9226dac308ac`，`RepoDigests` 为 `sentence-api@sha256:0cb1fd9412addf6a587c19ad42d31d79b9103a115f50c037527e9226dac308ac`；overview 本地 image ID `sha256:140219fa46db5c22e444ab48e623a7d6908a83e12cd200f4b68fc5f6638d27ed`，`RepoDigests` 为 `sentence-api@sha256:140219fa46db5c22e444ab48e623a7d6908a83e12cd200f4b68fc5f6638d27ed`。这些是本地 Docker inspect 结果，不等同于已发布 registry digest。未输出 Env 或密钥。
- [ ] 发布环境变量 `IMAGE_REGISTRY`、`IMAGE_NAME`、`REGISTRY_USERNAME`、`REGISTRY_PASSWORD`，受保护 `v*` 标签规则、`release` reviewer 和 `integration` 的 `MYSQL_TEST_DSN` 尚未由本地证据证明已配置。发布前必须由仓库管理员核实。
- [ ] 性能证据的正式镜像仍是 `d213967adfa7-dirty`；冻结提交重建后必须重新记录镜像 digest、版本、提交哈希和构建时间，并至少复跑相应 smoke。不能把旧 dirty 镜像证据直接移植为新 digest 证据。
- [x] 浏览器本地候选修复 v7 已部署到 Web 候选容器；12 个样本、1000 字完整复制、键盘 End 到达末尾、换句后滚动复位均已有候选证据，部署后公开与后台只读复验已通过。详见 [`docs/browser-acceptance.md`](browser-acceptance.md) 和 [`docs/deployment-20260920.md`](deployment-20260920.md)。
- [x] 已部署候选的本轮公开与后台只读复验通过：公开 12 样本、5 页 HTTP、CSS/JS hash、tabindex、键盘滚动行为通过；后台 14 页、cookie 安全属性、注销 replay 拒绝和无 pageerror 通过。投稿、审核及后台数据修改业务流未测；候选仍为 dirty 构建，不是正式 clean release。

## 可执行冻结、构建与发布步骤

1. 在工作副本外保存当前改动清单和必要证据；确认 `.cache/`、密钥、DSN、临时数据和结果目录不在候选提交中。用 `git status --short`、`git diff --check` 和 `git diff --stat` 复核。
2. 逐项审阅当前修改和未跟踪路径，只纳入本版本目标文件；确认迁移 `000003`、`000004` 与 Web/API 配套代码完整，删除或移出临时产物。生成一个干净提交，提交消息、提交哈希和变更清单写入发布记录。
3. 在该提交上执行 `gofmt` 检查、`go test ./...`、`go test -race ./...`、`go vet ./...`，再执行 CI 中列出的六个 fuzz 目标、外部 MariaDB 11.8 集成和 image smoke。标签前保留每个 job 的链接和结论。
4. 先构建不可变候选镜像，使用真实参数 `VERSION=vX.Y.Z`、`GIT_COMMIT=<40位提交>`、`BUILD_TIME=<UTC>`；记录本地 image ID。不得使用 `dev`、`unknown` 或 dirty 元数据作为发布产物。
5. 对这个候选镜像运行 `migrate up`、`scripts/smoke.sh`、`scripts/smoke-web.sh`，并核对 `/healthz`、`/readyz`、`/version`、分类、随机查询及 Web 管理入口。迁移前备份并确认数据库当前版本。
6. 候选 digest 通过本地验收后，按 CI 实际支持的发布流程执行推送和标签；不要把本地候选证据表述为已提升的正式制品。按兼容顺序滚动：先替换所有读 API 为候选镜像，再执行迁移到 schema 4，确认迁移成功后启动 Web（同一镜像的 `web` 子命令），最后由外部代理逐步切流。检查指标、日志、刷新状态和错误率。
7. 标签流水线产出的最终 digest 另行执行相应 image smoke 并记录；最终只部署该已验收 digest，不能以本地候选证据代替。生产引用 digest 或不可变发布标签，禁止使用 dirty 或浮动标签。

## 回滚步骤

先停止继续切流并保留失败实例日志、digest 和迁移状态。应用层通过外部代理切回上一个已验证且支持当前 schema 4 的 digest；这一步与数据库操作分开执行。不要在未完成兼容性评估、备份和演练前执行迁移降级；实际降级语法以项目 CLI 和运维文档为准，本文不臆测命令或自动删除 schema。数据恢复使用版本锁协议的补偿写入或数据库恢复；恢复后递增 dataset version 并执行手动 reload 或重启，避免相同版本号掩盖变更。回滚完成后复查 `/readyz`、读 API、Web、数据库指标和代理错误率。

## 审计结论

当前状态为“发布准备未完成”。性能和故障隔离验收已有通过记录，发布控制面和镜像运行策略也已读审；待冻结工作树、未核实的保护环境/registry 配置以及尚未产生的正式发布 digest，均阻止正式版本冻结和上线结论。完成上述待做项并获得受保护标签流水线的完整成功记录后，才能把版本标记为可发布。
