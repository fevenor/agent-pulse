# 镜像构建与部署

首次部署按 [部署指南](deployment.md) 执行。普通用户只使用完整 Hub 与 Relay 两个成品镜像，不需要理解或安装中间构建。Hub 使用 [`hub.image.yaml`](../deploy/compose/hub.image.yaml)，Relay 使用 [`relay.image.yaml`](../deploy/compose/relay.image.yaml)。

## 当前公开版本

[v0.5.0-rc.4](https://github.com/fevenor/agent-pulse/releases/tag/v0.5.0-rc.4) 已公开为预发布候选，Hub 与 Relay 均提供 Linux amd64/arm64 镜像：

- 完整 Hub：`ghcr.io/fevenor/agent-pulse-hub:v0.5.0-rc.4`，包含官方签名仓库和公钥。
- Relay：`ghcr.io/fevenor/agent-pulse-relay:v0.5.0-rc.4`。

两包均为公开包并关联本项目，支持匿名拉取。公开候选的标签和附件固定，不再沿用草稿滚动替换；后续变更应发布新版本。本版本不是稳定版，不推广稳定 `latest`。`candidate-manifest.json` 和 `trial-guide.md` 保留构建时快照，当前发布状态以 Release 页面为准。公开验证证据见[开发记录](development-status.md#2026-11-16-rc2-公开发布与完工检查)。

## 发布与签名规则

采用现有 Hub/Relay 分发和官方索引签名。版本号用于区分版本，SHA-256 用于核对文件，签名用于确认发布授权；版本和摘要不替代签名。贡献者只提交源码与测试，官方发布者审核、构建并统一签名。普通用户不生成私钥、不签适配包，也不自行组装 Hub。

发布私钥沿用发布者本机安全存放方式，不进入仓库、CI、Hub、Relay、构建目录或用户附件；已有公钥的设备继续沿用原密钥，索引序号递增。不引入账号、多维护者密钥体系或密钥管理服务。

GitHub Workflow 对 PR、默认分支、版本标签和手动触发执行检查。Go 编译器固定 1.27.4；Go module 的 1.25.0 仅为源码语言底线。两架构 Hub/Relay 均执行容器恢复、鉴权、离线去重、名称转发和已读检查；arm64 在 x86_64 上的模拟验证不等于真机。

## 成品命名与内部构建

本仓库的普通用户成品地址为：

- `ghcr.io/fevenor/agent-pulse-hub:<已发布版本>`
- `ghcr.io/fevenor/agent-pulse-relay:<已发布版本>`

仅 push 事件在检查通过后自动发布 Relay 的固定提交/版本标签，不更新稳定 `latest`。Hub 在签名仓库组装完成后由发布者发布同一版本成品，未完成签名不能冒充成品。

未签名 Hub 应用使用 `hub-build.tar` 保存在同一工作流的 `hub-build` Artifact 中，常规保留14天。OCI（开放容器镜像规范）归档能保存 amd64/arm64 的共同构建，不必额外推送第三个公开基础镜像；它是发布者内部材料，不是普通用户的安装目标。新的源码流程不会自动迁移或删除此前镜像标签，实际发布情况见开发记录。

## RC 试运行草稿

候选先创建 `v<工程版本>-rc.<序号>` 标签，再显式手动触发对应工作流；标签本身不再自动启动完整镜像发布。完整组件候选使用 `release-draft.yml`，仅 Bridge/Tray 修复候选使用 `release-clients.yml`，后者指定继续使用的公开 Hub/Relay 与适配包 Release。同一提交的 Linux 与 Windows 检查成功后创建草稿；已有公开 Release 不由重跑覆盖。

草稿用户附件仅包含 Windows 单入口便携包、两架构 Linux Bridge 与 SHA256SUMS；源码由 GitHub 标签归档提供。构建清单、中文维护说明、未签名仓库、发布工具与发布者专用 hub-build.tar 保存在对应工作流 Artifact，不混入用户下载列表。上传后核对实际附件名称、大小和摘要。

草稿中的构建归档与未签名包仅供发布者准备完整成品，不能让安装用户自行签名。新的适配包试运行需与同一现行 Hub/Relay/Bridge 构建配套，使用隔离数据并保留旧数据备份；不承诺旧候选通讯/存储兼容，不自动清空日常数据。正式公开和稳定版本推广仍由发布者明确决定。

## 按实际变更选择候选组件

只改客户端时可使用客户端候选工作流；0.6.0候选含Hub时间线及Bridge本机接口变更，使用完整候选构建。各组件按候选清单核验来源提交，Hub须在发布者附加同候选签名仓库后形成完整镜像。已有公开RC4的Hub/Relay镜像保持不可变。

Release用户附件仅为两架构Linux Bridge、Windows Tray ZIP与SHA256SUMS，源码由GitHub标签归档提供。安装脚本统一从 `https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh` 读取；构建清单、中文验证资料、未签名包与Hub构建归档保存在CI Artifact，仅供发布维护使用。公开推荐为RC4；未公开的0.6.0候选不得作为匿名默认安装目标。

修订草稿必须先确认draft=true，更新候选标签及对应构建、校验清单与说明，删除旧草稿多余附件；公开RC2/RC3不可覆盖。命令型包内容变化必须递增包版本及正式签名索引，原公钥保留。实际部署与手机测试按使用者授权执行。

## 准备普通用户 Hub RC 镜像

以下操作只面向官方发布维护者。先下载同一候选的构建归档和两平台适配包，按校验清单验证摘要。未签名仓库可来自 RC 附件或现有合并工具，不能混用不同候选的程序与包。首次正式发布才生成一套长期密钥，后续沿用已有密钥，不创建替代公钥。

```sh
# 使用已有长期密钥及新的正式索引序号签署本次完整平台包仓库。
# index-input.json 中的序号必须高于现有正式索引，草稿序号1只是占位值。
./bin/agent-pulse-adapters publish --repository ./dist/repository-combined \
  --private /安全目录/publisher.key --index ./dist/repository-combined/index-input.json

# HUB_BUILD_SHA256 取自同一候选的构建清单/校验清单，不从归档自己推断来源。
uv run --offline --no-project python scripts/release/prepare-hub-release.py \
  --repository ./dist/repository-combined --public /安全目录/publisher.pub \
  --base-archive ./dist/hub-build.tar --base-sha256 "$HUB_BUILD_SHA256" \
  --output ./dist/hub-release --tool ./bin/agent-pulse-adapters

# 实际发布是独立步骤，需发布者已有 GHCR write:packages 权限；prepare 本身不会推送。
uv run --offline --no-project python scripts/release/publish-hub-release.py \
  --manifest ./dist/candidate-manifest.json --repository ./dist/repository-combined \
  --public /安全目录/publisher.pub --archive ./dist/hub-build.tar \
  --tool ./bin/agent-pulse-adapters --output ./dist/hub-publish
```

准备脚本复用 Go 发布工具验签、核对包摘要与大小，只复制公开公钥、索引和不可变包；OCI 归档先核对摘要，拒绝路径越界、链接及非法文件类型。它不读取私钥，不构建、不推送镜像，不覆盖已有输出。命名上下文仅向 Buildx 提供本地 OCI 构建，最终镜像包含默认配置、网页和初始`/repository`；启动时仅首次复制到持久`/data/adapters`并固定公钥，后续活动仓库不被镜像覆盖，独立adapter发布无需重建Hub镜像，普通用户无需挂载包仓库或手填公钥。

发布采用 Linux 原生环境；Windows 可以进行脚本与目录准备回归，但不能替代 Linux 容器验收。已经由发布者验证的本地基准镜像仍可显式使用 `--base-image <镜像或digest>`，仅是维护入口，不作为第三种用户交付物。

完整 Hub 的发布步骤会核对候选提交与完整平台签名仓库、推送横杠成品的 RC 标签和不可变提交标签，并回读远端摘要及 amd64/arm64 平台索引。只在本地构建或装载不代表 GHCR Packages 已创建；须保留 publish-result.json 与远端验证结果。旧的 agent-pulse/hub、hub-base、relay 包不会因更改工作流自动删除，删除须使用者另行确认，不能把旧包误写成当前发布目标。

镜像仓库授权沿用现有 GitHub/容器工具登录，不另建账号服务。CI 的 Relay 推送使用仓库 `GITHUB_TOKEN` 的 packages 写权限；完整 Hub 推送使用发布者已有授权，私钥无需上传 GitHub。公开镜像可匿名拉取；尚未公开的草稿/镜像须已有读取权限。本地构建通过不表示远端工作流或公开发布已完成。

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

镜像地址形如 `ghcr.io/owner/agent-pulse-relay:sha-完整提交哈希`，须替换为实际发布值。默认仅暴露宿主 `127.0.0.1:18701`，使用 [Nginx 配置](../deploy/proxy/relay.nginx.conf) 提供公网 HTTPS/WSS。Hub 主动连接该地址；无需开放 Hub 公网入站。升级前备份数据卷，回退时指定之前已验证的镜像版本；不要使用 `down -v` 删除持久数据。

流程参考 [Docker 多架构构建说明](https://docs.docker.com/build/ci/github-actions/multi-platform/)；Node 环境使用 [Volta Action](https://github.com/volta-cli/action)，Python 测试通过 [setup-uv](https://github.com/astral-sh/setup-uv) 执行。
