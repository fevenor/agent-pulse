# Windows 原生开发

Windows 主工作副本使用本地 NTFS 目录、原生 PowerShell 和 Windows 版工具链。Rust + GPUI 桌面客户端、Go 服务和 Windows 安装器已具备开发入口；当前运行方法见 [Windows 客户端](windows-client.md)，验证证据及待验收项见 [开发记录](development-status.md)。不把任一开发机的路径、安装状态或私人设置当成项目要求。

## 工具链

| 用途 | 准备要求 |
| --- | --- |
| Go 服务与 Bridge | 源码语言兼容底线仍为 Go 1.25.0；自动发布构建与本轮验收固定 Go 1.27.4；脚本使用 `GOTOOLCHAIN=local`，不自动下载编译器 |
| 原生桌面 | Rustup，`tray/rust-toolchain.toml` 指定的工具链、MSVC 目标和组件；依赖以 `Cargo.lock` 为准 |
| C++ 链接 | Visual Studio Build Tools 的 C++ 桌面开发、MSVC x64/x86、Windows SDK；PowerShell 脚本通过 vswhere 定位安装目录 |
| Web / OpenCode | Volta 管理 Node.js 24.14.1、pnpm 10.33.2，版本由各 `package.json` 声明；只用 pnpm |
| Python 测试 | uv；用 `uv run` 执行，按需 `uv venv` / `uv pip` 管理环境 |
| Linux 联调 | Debian WSL2 内的 Docker Engine、Compose 与 Buildx；无需另装 Docker Desktop |

GPUI 固定为 0.2.2，托盘库固定为 tray-icon 0.26.1。使用仓库锁定依赖，不照搬完整 Zed 的全部构建依赖。CMake/Spectre 库只在实际构建要求时补充；当前具体验证条件写在开发记录，不能据此断言其他工具链组合一定可用。

Windows SDK 安装器为 Desktop C++ amd64 自动勾选的依赖保留即可；性能分析器、调试器、应用认证、ARM64 等按实际需要安装，不要求全选。SDK 提供头文件与库，不能替代 MSVC 编译器。安装工具后重新打开终端，核对 `go version`、`rustc -Vv`、`cargo -V`，避免 PATH 或 GOROOT 沿用旧环境。

Volta 可按项目版本下载工具，不需改全局默认：

```powershell
$env:VOLTA_FEATURE_PNPM = '1'
volta fetch node@24.14.1 pnpm@10.33.2
```

下载镜像失败时先确认具体请求，可对本次命令指定官方 registry；不要为了项目改变全局代理/镜像。根目录没有 `package.json`，根目录的 Node/pnpm 版本不能证明子项目已对齐。统一检查入口会读取各子项目版本并通过 `volta run` 选择。

## 开发准备与验收基线

从仓库根目录执行：

```powershell
./scripts/build/build.ps1 -DevelopmentTools
./scripts/check/check.ps1 -Tray -Integration
./scripts/build/build.ps1 -Demo
./bin/dev/agent-pulse-tray-demo.exe --demo
uv run --offline --no-project python tests/e2e/windows_desktop.py
```

`build.ps1` 默认只将发布 Tray 放入 `bin/`，内置 Bridge 在构建缓存中生成。`-DevelopmentTools` 显式将 Go 测试服务和发布工具放入 `bin/dev/`；`-Demo` 生成前台开发演示 `bin/dev/agent-pulse-tray-demo.exe`。`-Tray` 保留为兼容参数。`check.ps1` 执行 Go 测试和静态检查、锁定版本的 Web/OpenCode 安装与检查；`-Tray` 增加 Rust 检查，`-Integration` 增加真实本地 Hub/Relay 联调。只检查 Go 可用 `-GoOnly`。脚本逐项检查原生命令退出码；不能把 PowerShell 最后一个命令成功当作前面都通过。

Rust 补充静态检查：

```powershell
. ./scripts/build/windows-tools.ps1
Initialize-CppTools
Push-Location tray
try { cargo clippy --release --locked --all-targets -- -D warnings } finally { Pop-Location }
```

Windows 普通测试包含安装器回归，但没有 `go test -race`。Go 的 Windows 竞态检测要求兼容的 mingw-w64 C 编译器，MSVC 不能直接替代；当前继续由 Linux 执行竞态检查。官方要求见 [Go 竞态检测](https://go.dev/doc/articles/race_detector#Requirements)。

## Windows 钩子链路与适用范围

本轮 Windows RC 只支持 ZCode 原生进程钩子：程序路径和参数数组分开传入，版本探测直接读取 EXE 资源，两者不经过 PowerShell。Windows Codex CLI 和 ChatGPT Windows 桌面端只列后续规划，不默认扫描、不构建或发布其包；旧登记保留但不重新探测或更新命令型/插件型来源。

旧命令字符串回显和批处理版本探测曾执行编码 PowerShell，现已随非本轮功能移出当前执行回归。保留的命令构造/所有权纯测试只用于旧配置识别，不启动脚本。旧超时与防护反馈不改写为已解决，也不作为 ZCode 已有正常生命周期的失败证据；后续恢复对应来源时需重新设计并验收其真实链路，不恢复隔离文件或关闭防护。

原生参数与标准输入测试从实际 ZCode 配置生成入口取得 command/args，直接调用合法测试 EXE，核对 7 个参数及中文/emoji、前导空白、CRLF 输入字节。同步器回归同时确认后置来源既有配置不改写、没有安装状态重建。测试不调用在线模型，不代替真实宿主事件或手机实收。

源码语言底线不是发布标准库版本。若使用解压的局部 Go SDK，当前进程的 `GOROOT`、`PATH` 和 `GOTOOLDIR` 必须对应同一套 Go 1.27.4，不只替换 `go.exe` 而保留旧 `GOROOT`，也不必修改系统 Go 或全局配置。原生参数数组启动工具时应显式继承已有 `CARGO_HOME`、`RUSTUP_HOME` 和 `VOLTA_HOME`；可在验证进程设置 `RUSTUP_AUTO_INSTALL=0`，避免工具 shim 落入另一套默认缓存后自动下载。

保持唤醒验收结束后退出自建 Tray 并清理自建服务；系统执行状态是所有请求的汇总，不提供逐进程归属。若其他程序同时有请求，不能用汇总值替代当前 Tray 的单独释放证明，也不覆盖其他应用的请求来制造“清零”。

## 自动构建

`.github/workflows/windows.yml` 在 Windows Server 2022 runner 使用锁定工具链执行上述检查、本地 Hub/Relay 联调、发布和演示构建，以及二进制隔离和便携 ZIP 内容检查。构建产物包含便携 ZIP 和 ZCode 一个未签名 Windows 原生适配包，保留 14 天；Linux 来源及通用 OpenCode 插件保持由 Linux Workflow 构建，按 [适配包发布说明](adapters.md#发布者构建与签名) 合并。OpenCode 通用包不代表本轮支持 Windows 宿主。不上传原始测试日志、私钥或本机配置，不签名发布适配包，也不替代真实桌面交互验收。当前运行结果以对应提交的 Actions 为准，不能把 Workflow 文件存在视为已经通过。

## Debian 与容器

需要 WSL 时显式使用 `wsl -d Debian`。Windows UI、原生构建和 UI 测试留在 Windows；Linux 编译、容器与服务测试放在 Debian 文件系统的独立目录。两侧只交换源码和构建需要的文件，不复用 `node_modules`、Rust target 或 Go 二进制。

在 Debian 的工作副本中，Web 构建后执行：

```sh
cd go
GOTOOLCHAIN=local go test -race ./...
GOTOOLCHAIN=local go vet ./...
cd ..
docker build --target hub -t agent-pulse-hub:windows-dev -f deploy/docker/Dockerfile .
docker build --target relay -t agent-pulse-relay:windows-dev -f deploy/docker/Dockerfile .
uv run --no-project python tests/e2e/relay_containers.py --arch amd64 --hub-image agent-pulse-hub:windows-dev --relay-image agent-pulse-relay:windows-dev
```

也可以将 Go 检查放入官方 Go 容器；测试副本须同时包含 `go/` 和 `protocol/`，否则契约测试找不到样例。临时 Shell 脚本必须使用 LF。Docker 上下文排除 `.cache` 和 `tray/target`，避免传输本机缓存。

跨 Windows / Debian Bridge 检查先构建 Windows 程序和上述镜像，再在 PowerShell 中准备 Linux Bridge：

```powershell
$previousOS = $env:GOOS
$previousArch = $env:GOARCH
try {
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    New-Item -ItemType Directory -Force .cache/windows-check | Out-Null
    Push-Location go
    try { go build -trimpath -buildvcs=false -o ../.cache/windows-check/bridge-linux ./cmd/agent-pulse-bridge } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { throw 'Linux Bridge 构建失败' }
} finally {
    $env:GOOS = $previousOS
    $env:GOARCH = $previousArch
}
uv run --offline --no-project python tests/e2e/windows_wsl.py
```

该测试只使用隔离来源、临时配置和回环端口，测试结束清理所创建的容器与进程；需要 Windows 可访问 WSL 本地转发。两来源使用不同 `source_id`，防止同名会话合并。

## 后续验收

继续沿用 Rust + GPUI，不因一次构建或网络故障切换框架。只有出现无法合理解决的关键桌面行为或维护障碍时，按已确认方案评估 Go + Walk。Bridge 保持 Go，不提前重写其他组件。

桌面实测仍需覆盖：锁屏/解锁、睡眠唤醒、真实全屏、卡片外观、托盘点击/菜单、多显示器/DPI、远程桌面和长期运行。当前状态轮询的延迟、失联期间卡片行为需与实际使用体验一起验收。宿主方面只验证本轮 ZCode 图形界面的实际加载；Windows Codex CLI 后置；不自动改写日常配置或调用真实模型、手机渠道。

详情快捷偏好、唤醒退出、通知分流及悬浮入口的剩余原生检查在 RC 发布后长期运行中补验，不作本轮发布阻断，不能因此改写已有未验收结论。

发布构建使用 `scripts/build/package-windows.ps1`；包只用于可解压运行，不隐式注册自启动或部署公网服务。自动化测试与原生验收分别记录。Git 读操作设置 `GIT_OPTIONAL_LOCKS=0`；暂存、提交、推送和其他 Git 写操作继续按用户单独授权范围执行。
