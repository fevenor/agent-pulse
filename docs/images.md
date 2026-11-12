# 镜像构建与部署

首次部署按 [部署指南](deployment.md) 执行。Hub 使用 [`hub.image.yaml`](../deploy/compose/hub.image.yaml)，Relay 使用 [`relay.image.yaml`](../deploy/compose/relay.image.yaml)；本页说明发布规则及镜像验证。

[GitHub Workflow](../.github/workflows/images.yml) 负责 Hub 与 Relay 的检查和基础镜像构建。PR、main/master 推送、`v*` 标签和手动触发均执行检查；仅推送事件在检查通过后发布至 GHCR。Relay 是直接可部署镜像；Hub 自动构建标签为 `hub-base`，它包含默认运行配置和网页，但尚无 RC 对应的签名适配仓库，不能当作普通用户发布镜像。正式 Hub RC 由发布者在本地签名后，使用本页的准备脚本附加仓库并推送版本标签。手动触发只验证，不发布。

## 构建与标签

流程先运行 Go 竞态/静态检查、OpenCode 类型与映射测试、Vue 生产构建，再构建 `linux/amd64`、`linux/arm64` 的 Hub/Relay。两种架构均启动真实容器，验证鉴权、离线队列、崩溃恢复、反向通道、页面和已读；arm64 使用 QEMU 模拟。镜像使用非 root 用户，SQLite 存在独立数据卷。

自动构建的基础镜像地址按当前仓库自动生成，并转成小写。本仓库为 `fevenor/agent-pulse`：

- `ghcr.io/fevenor/agent-pulse/hub-base:sha-<完整提交哈希>`
- `ghcr.io/fevenor/agent-pulse/relay:sha-<完整提交哈希>`

默认分支另发布 `hub-base:latest` 和 `relay:latest`；`v*` 标签另发布基础 Hub 与 Relay 同名版本。只有完成本地签名与镜像附加步骤后，发布者才将完整 Hub 镜像标记为普通用户使用的 `hub:<RC版本>`。部署固定完整 RC 标签，不能使用 `hub-base`。

## 准备普通用户 Hub RC 镜像

发布者需先从对应提交的 Linux 和 Windows 工作流下载 `linux-adapters` 与 `windows-amd64` 包。首次使用时在私有安全目录生成一套发布密钥并长期保管；以后沿用同一密钥，不将私钥放入仓库、构建目录、镜像或 CI 产物。

在源码根目录合并平台包并使用递增序号生成索引：

```sh
uv run --offline --no-project python scripts/merge_adapter_repositories.py \
  ./dist/repository-linux ./dist/repository-windows \
  --output ./dist/repository-combined --sequence 1
./bin/agent-pulse-adapters keygen --private /安全目录/publisher.key --public /安全目录/publisher.pub
./bin/agent-pulse-adapters publish --repository ./dist/repository-combined \
  --private /安全目录/publisher.key --index ./dist/repository-combined/index-input.json
```

后续发布将序号递增。首次发布可在同一安全目录创建密钥；已经有正式公钥的部署必须继续使用原密钥。然后使用签名仓库、公钥与匹配提交构建的 `hub-base` 镜像准备独立 Docker 构建目录：

```sh
uv run --offline --no-project python scripts/prepare-hub-release.py \
  --repository ./dist/repository-combined --public /安全目录/publisher.pub \
  --base-image ghcr.io/fevenor/agent-pulse/hub-base:rc版本 \
  --output ./dist/hub-release --tool ./bin/agent-pulse-adapters
docker buildx build --platform linux/amd64,linux/arm64 --push \
  --tag ghcr.io/fevenor/agent-pulse/hub:rc版本 ./dist/hub-release
```

脚本使用 Bridge 同一验签工具核对索引签名、包摘要与大小，将 `publisher.pub` 和索引引用的不可变包复制到新的构建目录，拒绝损坏仓库和已存在的输出目录。它不读取私钥、不签名、不构建或推送镜像；最终 `docker buildx` 步骤需发布者显式执行。完整镜像包含默认 Hub 配置、网页和 `/repository`，Hub 自动读取随仓库提供的 `publisher.pub`；显式 `adapter_public_key` 仍优先。普通用户部署已签名 RC 时无需挂载仓库或配置公钥。

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
