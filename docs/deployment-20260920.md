# Web 候选部署记录

日期：2026-09-20。范围仅为 Web 容器；读 API、MariaDB、OpenResty 和 `1panel-network` 未修改。

## 制品与运行配置

- 镜像标签：`sentence-api:web-candidate-20260920t2158z`
- 本地 image ID：`sha256:ba2bdae368bf8645dfa8f96e83d0207f9f2b24461db93e435dcc35da11a2cced`
- 版本：`web-candidate-20260920`
- Git 元数据：`d213967adfa71b2c694b4b1eee7b6df6e78b68e3-dirty`
- 构建时间：`2026-09-19T21:51:44Z`
- 活跃容器：`sentence-web-test`，`1panel-network`，`172.16.99.100:18081 -> 8081`，用户 `65532:65532`，重启策略 `unless-stopped`
- 回滚容器：`sentence-web-test-rollback-web-candidate-20260920t2158z`，停止状态，旧镜像 `sentence-api:release-fixes-20260919t190026z`
- 新旧 Web 的 Env 键和值比较一致；报告不记录任何 Env 值。

## 执行与回滚

构建使用现有 Dockerfile 和缓存，参数为 `VERSION=web-candidate-20260920`、当前 dirty Git 标识和 UTC build time。候选先以独立容器名和 `127.0.0.1:18082 -> 8081` 启动，加入原网络，复用原配置，检查 `/healthz`、`/readyz`、`/version`、根页面及 CSS/JS 静态资源。

第一次候选脚本把 Docker `-e` 参数放在镜像和命令之后，配置未注入，候选以 `configuration-or-runtime` 退出并自动清理；正式 Web未受影响。第二次候选使用正确参数顺序通过。第一次正式切换的探针错误访问 `127.0.0.1:18081`，而宿主绑定地址为 `172.16.99.100:18081`，脚本自动回滚并恢复旧容器。第二次使用正确绑定地址切换成功。

切换顺序是停止旧容器、改名保留回滚、启动同名新容器；失败时删除新容器、改回旧名、恢复 `unless-stopped` 并启动旧容器。成功后 `/healthz`、`/readyz`、`/version` 均返回 200，ready dataset version 为 4。

这是 dirty 工作树候选部署，不是正式 clean release，也不是 registry 发布镜像。本轮公开与后台只读验收已完成并通过，但不能据此宣称正式 clean release 完成。

## 部署后验收

部署后公开报告 `.cache/deployed-browser-20260920/public/report.json` 的 12 个样本、5 个公开页面、CSS/JS hash、tabindex、复制和键盘滚动检查均通过。后台 `admin-report` 的 14 个页面及 cookie Secure/HttpOnly/SameSite=Strict、注销后旧 session replay 拒绝检查均通过，未见 pageerror。投稿、审核、用户/句子/分类/站点设置修改等业务操作流未测。该制品仍带 dirty Git 元数据，不能称为正式 clean release。
