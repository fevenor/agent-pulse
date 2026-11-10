# 镜像构建与部署

首次部署按 [部署指南](deployment.md) 执行。Hub 使用 [`hub.image.yaml`](../deploy/compose/hub.image.yaml)，Relay 使用 [`relay.image.yaml`](../deploy/compose/relay.image.yaml)；本页说明发布规则及镜像验证。

[GitHub Workflow](../.github/workflows/images.yml) 负责 Hub 和 Relay 的检查、构建与发布。PR、main/master 推送、`v*` 标签和手动触发均执行检查；仅推送事件在全部检查通过后发布至 GitHub Container Registry（GHCR，GitHub 提供的容器镜像仓库）。手动触发只验证，不发布。

## 构建与标签

流程先运行 Go 竞态/静态检查、OpenCode 类型与映射测试、Vue 生产构建，再构建 `linux/amd64`、`linux/arm64` 的 Hub/Relay。两种架构均启动真实容器，验证鉴权、离线队列、崩溃恢复、反向通道、页面和已读；arm64 使用 QEMU 模拟。镜像使用非 root 用户，SQLite 存在独立数据卷。

发布地址按当前仓库自动生成，并转成小写。本仓库为 `fevenor/agent-pulse`：

- `ghcr.io/fevenor/agent-pulse/hub:sha-<完整提交哈希>`
- `ghcr.io/fevenor/agent-pulse/relay:sha-<完整提交哈希>`

默认分支另发布 `latest`；`v*` 标签另发布同名版本。部署推荐固定提交标签或镜像摘要，避免跟随 `latest` 意外升级。生产部署不在 Workflow 中自动执行。

使用仓库自带 `GITHUB_TOKEN` 的 packages 写权限，无需额外保存镜像仓库密码。私有镜像拉取需部署主机已有 GHCR 读取授权；公开镜像可匿名拉取。第一次远端运行仍需检查仓库 Actions 权限与镜像可见性。本地构建通过不代表 GitHub 上已执行或已发布。

## 本地执行相同验证

安装 Volta 管理的 Node.js/pnpm、Go、uv 和 Docker，先构建页面，再执行：

```sh
pnpm --dir web/hub install --frozen-lockfile --ignore-scripts
pnpm --dir web/hub build
for arch in amd64 arm64; do
  for component in hub relay; do
    docker buildx build --platform "linux/$arch" --target "$component" \
      -f deploy/docker/Dockerfile --load -t "agent-pulse-$component:test-$arch" .
  done
  uv run --offline --no-project python tests/e2e/relay_containers.py \
    --arch "$arch" --hub-image "agent-pulse-hub:test-$arch" \
    --relay-image "agent-pulse-relay:test-$arch"
done
```

构建阶段由本机 Go 交叉编译，不要求 QEMU；在 amd64 上运行 arm64 容器则需要主机已注册 QEMU。Workflow 通过 Docker 官方 Action 准备模拟器。本地脚本不修改主机模拟器配置，缺少时会明确测试失败。构建不会调用真实 Agent 模型或手机渠道。

## 部署 Relay 已发布镜像

部署目录只需 `relay.image.yaml` 和 `relay.runtime.yaml`；数据卷独立保留。配置中继令牌和镜像地址，例如将环境文件保存在仓库外、权限设为 600，包含 `AGENT_PULSE_RELAY_TOKEN` 和 `AGENT_PULSE_RELAY_IMAGE` 两项。令牌用随机值，不要把实际值放进命令历史或版本控制。

```sh
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml pull
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml up -d
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml ps
```

镜像地址形如 `ghcr.io/owner/agent-pulse/relay:sha-完整提交哈希`，须替换为实际发布值。默认仅暴露宿主 `127.0.0.1:18701`，使用 [Nginx 配置](../deploy/proxy/relay.nginx.conf) 提供公网 HTTPS/WSS。Hub 主动连接该地址；无需开放 Hub 公网入站。升级前备份数据卷，回退时指定之前已验证的镜像版本；不要使用 `down -v` 删除持久数据。

流程参考 [Docker 多架构构建说明](https://docs.docker.com/build/ci/github-actions/multi-platform/)；Node 环境使用 [Volta Action](https://github.com/volta-cli/action)，Python 测试通过 [setup-uv](https://github.com/astral-sh/setup-uv) 执行。
