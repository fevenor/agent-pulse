# 镜像构建与部署

首次部署按 [部署指南](deployment.md) 执行。Hub 使用 [`hub.image.yaml`](../deploy/compose/hub.image.yaml)，Relay 使用 [`relay.image.yaml`](../deploy/compose/relay.image.yaml)；本页说明发布规则及镜像验证。

[GitHub Workflow](../.github/workflows/images.yml) 负责 Hub 与 Relay 的检查和基础镜像构建。PR、main/master 推送、`v*` 标签和手动触发均执行检查；仅推送事件在检查通过后发布至 GHCR。Relay 是直接可部署镜像；Hub 自动构建标签为 `hub-base`，它包含默认运行配置和网页，但尚无 RC 对应的签名适配仓库，不能当作普通用户发布镜像。正式 Hub RC 由发布者在本地签名后，使用本页的准备脚本附加仓库并推送版本标签。手动触发只验证，不发布。

发布前用于集中验证的完整版本称为**预发布候选版本（RC）**。发布编译器与 Go 标准库固定为 Go 1.27.4，`go/go.mod` 的 Go 1.25.0 仅保留源码语言兼容底线；`golang.org/x/sys` 使用最小修复版本 0.44.0。固定版本不能代替真实二进制构建信息核验，也不表示既有远端镜像已经更新。

## 构建与标签

流程先运行 Go 竞态/静态检查、OpenCode 类型与映射测试、Vue 生产构建，再构建 `linux/amd64`、`linux/arm64` 的 Hub/Relay。两种架构均启动真实容器，验证鉴权、离线队列、崩溃恢复、反向通道、页面和已读；arm64 使用 QEMU 模拟。镜像使用非 root 用户，SQLite 存在独立数据卷。

自动构建的基础镜像地址按当前仓库自动生成，并转成小写。本仓库为 `fevenor/agent-pulse`：

- `ghcr.io/fevenor/agent-pulse/hub-base:sha-<完整提交哈希>`
- `ghcr.io/fevenor/agent-pulse/relay:sha-<完整提交哈希>`

默认分支仅发布提交 SHA 标签，不在试运行前更新稳定 `latest`；版本标签发布基础 Hub 与 Relay 的同名版本。只有完成本地签名与镜像附加步骤后，发布者才将完整 Hub 镜像标记为普通用户使用的 `hub:<RC版本>`。部署固定完整 RC 标签，不能使用 `hub-base`。

## RC 试运行草稿

推送 `v<工程版本>-rc.<序号>` 标签触发 `release-draft.yml`，复用同一提交的 Linux 与 Windows 工作流；只有两类检查及产物构建均成功才创建 GitHub Release 草稿。草稿保持预发布标记，不自动转正、不更新稳定镜像别名。已被人工发布或改为其他状态的 Release 不由重跑覆盖。

草稿附 Windows 单入口便携包、五包未签名仓库、Linux 两架构 Bridge 预构建与安装脚本、发布工具、构建清单、摘要和中文试运行说明。私有配置和发布私钥不进入 CI 或附件；没有长期密钥时不生成替代公钥，不把基础 Hub 镜像冒充完整签名 RC。发布者可沿用现有 Hub/已签名仓库试运行客户端，或在 Linux 本机使用已有长期密钥签名，再按下文准备完整 Hub 镜像。Bridge 由同一 Linux 工作流预先构建，草稿生成只核验并复制产物；安装器从公开 Release 自动下载，不要求目标用户编译。旧草稿不会因本地修改自动补齐附件。草稿仅发布固定 SHA/RC 标签，人工运行数日后的发布与稳定版本推广由发布者单独决定。

## 准备普通用户 Hub RC 镜像

发布者需先从对应提交的 Linux 和 Windows 工作流下载 `linux-adapters` 与 `windows-amd64` 包。首次使用时在私有安全目录生成一套发布密钥并长期保管；以后沿用同一密钥，不将私钥放入仓库、构建目录、镜像或 CI 产物。

在源码根目录合并平台包并使用递增序号生成索引：

```sh
uv run --offline --no-project python scripts/release/merge_adapter_repositories.py \
  ./dist/repository-linux ./dist/repository-windows \
  --output ./dist/repository-combined --sequence 1
./bin/agent-pulse-adapters keygen --private /安全目录/publisher.key --public /安全目录/publisher.pub
./bin/agent-pulse-adapters publish --repository ./dist/repository-combined \
  --private /安全目录/publisher.key --index ./dist/repository-combined/index-input.json
```

后续发布将序号递增。首次发布可在同一安全目录创建密钥；已经有正式公钥的部署必须继续使用原密钥。然后使用签名仓库、公钥与匹配提交构建的 `hub-base` 镜像准备独立 Docker 构建目录：

```sh
uv run --offline --no-project python scripts/release/prepare-hub-release.py \
  --repository ./dist/repository-combined --public /安全目录/publisher.pub \
  --base-image ghcr.io/fevenor/agent-pulse/hub-base:rc版本 \
  --output ./dist/hub-release --tool ./bin/agent-pulse-adapters
docker buildx build --platform linux/amd64,linux/arm64 --push \
  --tag ghcr.io/fevenor/agent-pulse/hub:rc版本 ./dist/hub-release
```

正式发布采用 Linux 发布机流程。Windows 辅助目录准备在完整仓库上仍有原子改名拒绝访问的已知验证边界，不能用小仓库测试推断稳定；当前结果见[开发记录](development-status.md)。

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

构建阶段由本机 Go 交叉编译，不要求 QEMU；在 amd64 上运行 arm64 容器需要主机已有模拟器，或显式提供下文的用户态模拟器。Workflow 通过 Docker 官方 Action 准备模拟器。本地测试不修改主机模拟器配置；不提供模拟器时不能把 arm64 构建通过当成运行通过。构建不会调用真实 Agent 模型或手机渠道。

### 不注册内核处理器的模拟运行

`--emulator-file` 接收已校验来源、可执行且与 Docker 主机 CPU 架构匹配的静态 QEMU 文件。可将官方软件包下载到私有验证目录，核对大小与摘要后仅解包，不安装系统包。测试只读挂载该文件，在容器启动边界保留镜像原始程序和参数；非 root 用户、鉴权、持久队列、崩溃恢复与已读断言不变。它不注册内核二进制格式处理器（binfmt），也不需要特权安装容器。

```sh
uv run --offline --no-project python tests/e2e/relay_containers.py \
  --arch arm64 --hub-image agent-pulse-hub:test-arm64 \
  --relay-image agent-pulse-relay:test-arm64 \
  --emulator-file /验证目录/qemu-aarch64
```

文件路径须能被 Docker 主机读取。未指定该参数时仍使用镜像默认启动方式；显式模拟运行通过只证明同一 ARM 二进制在该模拟条件下工作，不代表 ARM 真机验收。

## 部署 Relay 已发布镜像

部署目录只需 `relay.image.yaml` 和 `relay.runtime.yaml`；数据卷独立保留。配置中继令牌和镜像地址，例如将环境文件保存在仓库外、权限设为 600，包含 `AGENT_PULSE_RELAY_TOKEN` 和 `AGENT_PULSE_RELAY_IMAGE` 两项。令牌用随机值，不要把实际值放进命令历史或版本控制。

```sh
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml pull
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml up -d
docker compose --env-file /安全目录/relay.env -f deploy/compose/relay.image.yaml ps
```

镜像地址形如 `ghcr.io/owner/agent-pulse/relay:sha-完整提交哈希`，须替换为实际发布值。默认仅暴露宿主 `127.0.0.1:18701`，使用 [Nginx 配置](../deploy/proxy/relay.nginx.conf) 提供公网 HTTPS/WSS。Hub 主动连接该地址；无需开放 Hub 公网入站。升级前备份数据卷，回退时指定之前已验证的镜像版本；不要使用 `down -v` 删除持久数据。

流程参考 [Docker 多架构构建说明](https://docs.docker.com/build/ci/github-actions/multi-platform/)；Node 环境使用 [Volta Action](https://github.com/volta-cli/action)，Python 测试通过 [setup-uv](https://github.com/astral-sh/setup-uv) 执行。
