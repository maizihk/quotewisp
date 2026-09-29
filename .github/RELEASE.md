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

目标为 `docker.io/maizihk/quotewisp`。在仓库 Settings → Secrets and variables → Actions 配置：

| 类型 | 名称 | 值 |
| --- | --- | --- |
| Variable | `DOCKERHUB_USERNAME` | `maizihk` |
| Variable | `DOCKERHUB_IMAGE` | `maizihk/quotewisp` |
| Secret | `DOCKERHUB_TOKEN` | 对目标仓库有读写权限的 Docker Hub 访问令牌 |

令牌仅提供给 publish 任务，不写入源码、构建参数或镜像。无需 GitHub Packages 权限、付费标签保护或环境审批。Actions 需已启用且有可用额度。

发布前确认目标版本尚未占用；已有版本应使用新版本号。私有镜像部署端需要自己的 Docker Hub 读取凭证。工作流不修改 Docker Hub 仓库可见性。

镜像包含版本、完整提交 SHA 和 UTC 构建时间，同时写入版本标签和提交标签。同一标签的发布串行执行，但重跑仍可替换镜像标签；部署使用已验收的 digest。若推送后的检查失败，该镜像不能用于部署。
