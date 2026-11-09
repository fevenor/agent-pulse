# Windows 原生开发接续

Windows 阶段先验证 Rust + GPUI 的桌面小组件原型，再实现 Tray 和原生 Bridge 管理。Hub/Relay 已有 Linux 镜像，原型可连接现有 Linux Hub，不必先在 Windows 重建全部后端环境。当前 `tray/` 尚无 Cargo 工程；已有 Windows Go 交叉编译产物不代表桌面和安装功能已经完成。

## 工作副本位置

主要工作副本放在 Windows 本地 NTFS 目录中，在 Windows 原生编辑器、PowerShell 或 Visual Studio 开发者终端中打开，使用 Windows Rust、Go、Node 和 Git。

```powershell
git clone git@github.com:fevenor/agent-pulse.git agent-pulse
Set-Location agent-pulse
```

若需要 Linux 服务端或 WSL Bridge 联调，在 WSL2 的 Linux 文件系统中另建副本，通过提交和推送同步。两个系统各自安装依赖，分别维护 node_modules、Cargo target、二进制和运行配置。主构建使用所属系统的原生文件系统，不共用跨系统的依赖缓存。

文件位置跟随实际执行工具的系统；这是 [微软 WSL 文件系统建议](https://learn.microsoft.com/en-us/windows/wsl/filesystems)。仓库用 `.gitattributes` 固定文本 LF，Windows `.bat/.cmd` 使用 CRLF，不需要为迁移修改全局 Git 换行配置。

## 最小环境准备

- Git for Windows 与可访问本仓库的 SSH 配置。
- Rustup、Cargo 和 Windows MSVC 目标 `x86_64-pc-windows-msvc`。GPUI 原型建立时固定具体工具链和依赖版本。
- Visual Studio Build Tools 的“使用 C++ 的桌面开发”、MSVC x64/x86、Windows SDK；按锁定 GPUI 版本补充 CMake/Spectre 库等实际依赖。使用开发者终端启动构建。
- Go 1.25 或兼容版本，用于 Windows Bridge 和命令行客户端。
- 修改 Web/OpenCode 时才准备 Windows 版 Volta、Node.js 24.14.1、pnpm 10.33.2；执行 Python 测试时使用 uv。

不需要安装 C#/.NET/WPF 工具链。Docker Desktop/WSL2 只在本地跑 Linux 服务或联调时需要，GPUI 原型可以直接连接已有 Hub。GPUI 环境参考 [Zed 官方 Windows 构建说明](https://zed.dev/docs/development/windows)，该文面向完整 Zed，独立小组件只安装实际依赖，不照搬完整应用的所有组件。

仓库当前 `scripts/*.sh` 和 `tests/e2e/*.py` 主要面向 Linux；不要把它们全部通过 WSL 执行并据此认定 Windows 原生通过。在 PowerShell 可先构建已有 Go 入口：

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
Push-Location go
go build -trimpath -buildvcs=false -o ../bin/ ./cmd/agent-pulse-bridge ./cmd/agent-pulse-client
Pop-Location
```

## 接续实施顺序

1. 读取 [开发记录](development-status.md)、[Tray 设计与原型条件](agent-pulse-design.md#3-tray-技术路线与验证) 和 [协议](../protocol/README.md)。沿用已确认选型，不重新搭建多套客户端。
2. 建立最小 GPUI 原型：托盘驻留、中文提醒卡片、简单列表、隐藏和退出；实测不抢焦点、锁屏/解锁、全屏、多显示器/DPI及远程桌面表现。
3. 记录发布构建的内存、CPU/GPU、冷启动和依赖准备成本。满足设计条件后再完成 Hub/Relay 连接、离线已读与缓存、通知规则和 Bridge 子进程管理。
4. 实现 Windows 适配包安装：当前非 Linux 安装锁明确拒绝使用；需要补充 Windows 文件锁、原子替换、路径/命令引用、可执行入口及宿主配置测试。不能把已有 Windows Bridge 编译通过当作自动安装器可用。
5. 完成 Windows 原生 Agent 与 WSL2 独立 Bridge 联调，配置不同 `source_id`，核对通知去重、已读及锁屏行为。

GPUI 遇到已记录且无法合理解决的关键阻碍时，按既定方案转 Go + Walk；Bridge 仍保持 Go。zcode 挂起，Codex 高级事件、手机多渠道优先级和物理多机长期更新后置。

迁移只需源代码和必要私有运行配置；不要复制 Linux bin/dist/node_modules 作为 Windows 依赖。手机密钥、中继令牌和发布私钥仍存仓库外。代理的用户级规则需在新环境单独配置，不随项目克隆迁移。Git 后续写操作继续按用户授权范围执行。
