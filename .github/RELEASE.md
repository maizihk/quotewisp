# 版本发布

工作流适用于个人免费仓库。普通提交、PR 和标签推送执行检查；发布需要手动触发。

## 发布步骤

1. 将工作流合入默认分支 `main`，推送待发布提交及对应的 `vMAJOR.MINOR.PATCH` 标签。
2. 对该标签运行：

   ```sh
   gh workflow run ci.yml --repo maizihk/quotewisp --ref v1.0.0 -f publish=true
   ```

3. 等待 quality、fuzz、integration、image 全部通过。publish 会构建并推送镜像，再按 digest 拉取，检查版本信息、API/Web 与 SQLite 持久化。
4. 从流水线摘要记录已验收的 digest，按 [运行文档](../docs/operations.md) 备份并部署。

默认 `publish=false` 只执行检查。分支和预发布标签不能执行正式发布。工作流不会自动部署或创建 GitHub Release 页面。

## 镜像仓库

默认目标为 `ghcr.io/maizihk/quotewisp`，通用命名规则为小写的 `ghcr.io/<owner>/<repository>`。publish 使用内置 `GITHUB_TOKEN` 和 `packages: write` 权限，无需额外发布密钥。

新包默认私有；已有同名包需授予本仓库 Actions 写权限。私有镜像部署端需要自己的读取凭证。Actions 需已启用且有可用额度。

镜像包含版本、完整提交 SHA 和 UTC 构建时间，同时写入版本标签和提交标签。同一标签的发布串行执行，但重跑仍可替换镜像标签；部署使用已验收的 digest。若推送后的检查失败，该镜像不能用于部署。
