# 版本发布

代码、版本标签和 Release 发布到 GitHub，镜像发布到 Docker Hub。工作流适用于个人免费仓库；普通提交、PR 和标签推送执行检查。

## 本地发布

使用本地 Docker Hub 登录凭据发布，无需将令牌上传到 GitHub。

1. 推送代码到 `main`，等待 GitHub CI 全部通过。
2. 从该提交的 `git archive` 构建镜像，设置 `VERSION`、完整 `GIT_COMMIT` 和 UTC `BUILD_TIME`。先推送 `git-<完整提交 SHA>` 镜像标签。
3. 按 Docker Hub 返回的 digest 拉取镜像，运行 `scripts/smoke-release-image.py` 验证版本、API/Web 和 SQLite 持久化。
4. 为同一提交创建并推送带注释的版本标签，为已验证镜像推送对应版本标签，核对远端 digest。
5. 创建 GitHub Release，记录镜像地址、digest、平台和验证结果。部署前按 [运行文档](../docs/operations.md) 备份。

## 可选：通过 GitHub Actions 发布

1. 将工作流合入默认分支 `main`，推送待发布提交及对应的 `vMAJOR.MINOR.PATCH` 标签。
2. 对该标签运行：

   ```sh
   gh workflow run ci.yml --repo maizihk/quotewisp --ref v1.0.1 -f publish=true
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

发布前确认目标版本尚未占用；已有版本应使用新版本号。镜像仓库若设为私有，部署端需要自己的 Docker Hub 读取凭证。工作流不修改 Docker Hub 仓库可见性。

镜像包含版本、完整提交 SHA 和 UTC 构建时间，同时写入版本标签和提交标签。同一标签的发布串行执行，但重跑仍可替换镜像标签；部署使用已验收的 digest。若推送后的检查失败，该镜像不能用于部署。
