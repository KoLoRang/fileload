# FileLoad — SSH 云端目录映射本地盘

把 Linux 服务器上的目录通过 SSH/SFTP 映射到本地：Windows 上是盘符（如 `Z:`），macOS 上是一个本地目录（默认 `~/FileLoad/<远程目录名>`）。云端文件可直接用本地程序打开、编辑、执行代码，改动自动回传服务器。

## 下载安装

安装包在 [GitHub Releases 页面](https://github.com/KoLoRang/fileload/releases)，每个版本提供：

| 系统 | 文件 | 说明 |
| --- | --- | --- |
| Windows 10/11 x64 | `FileLoad-Setup-<版本>.exe` | 多合一安装包：FileLoad + WinFsp 驱动一次装好 |
| macOS 13+ | `FileLoad-<版本>-macos.dmg` | 把 FileLoad.app 拖入「应用程序」；内置 FUSE-T 安装包，首次挂载时提示一键安装（需输入电脑密码） |

程序启动后会自动检查新版本（「关于」导航出现红点），在「关于」页点「立即更新」即自动下载、安装并重启。仓库为私有时，若构建时未内置令牌，请在「关于」页填写一个仅授权本仓库、Contents 只读的 GitHub fine-grained 令牌。

**运行依赖**

- Windows：[WinFsp](https://winfsp.dev/rel/)（内核驱动，FileLoad 靠它创建盘符，安装包已自带）；界面依赖系统自带的 [WebView2 运行时](https://developer.microsoft.com/microsoft-edge/webview2/)（Windows 11 与已更新的 Windows 10 均已内置）
- macOS：[FUSE-T](https://www.fuse-t.org)（无需内核扩展，app 内置安装包）；命令行功能需要系统自带的 `ssh`

> WinFsp 为什么不能直接打包进 FileLoad.exe？
> 它是**内核态驱动**（.sys 文件 + Windows 服务），必须装入系统并加载，用户态 exe 无法内嵌替代。所以安装包会在需要时静默安装 WinFsp MSI，已装过则跳过。

## 功能

- **原生风格界面**：左侧导航分「目录挂载」「命令挂载」「关于」；跟随系统深浅色，Windows 11 22H2+ 使用 Mica 背景；macOS 菜单栏图标
- **三步向导**：配置 SSH/SFTP 服务器 → 选择远程目录 → 选择挂载位置（Windows 盘符 / macOS 目录，已占用的自动避开）
- 连接成功自动保存服务器配置，下次下拉直接选
- **挂载记录**：每次挂载都会记录，启动时可在列表里一键重新挂载（静默重连，免向导）
- 远程目录浏览器（进入/选此目录/上级/刷新/路径直达）
- **读缓存**：首次访问整文件下载到本地缓存，之后秒开
- **写回传**：写入先落本地缓存并标记 dirty，后台队列自动上传回远端；卸载时同步冲刷
- **命令挂载**：挂载期间在本机命令行注入命令，所有命令都先 SSH 到服务器并进入挂载目录再执行：内置 `rcd` = 直接停在挂载目录的远程 shell；还可在「命令挂载」页添加自定义命令（命令名 + 远程命令）。走内置 SSH 代理免密（密码登录也免输）；在挂载目录的子目录内执行会进入对应的远程子目录；命令目录加入 PATH 时**前置**，避免与本机同名工具冲突；卸载/退出自动清理
- 断线自动重连 + 15s keepalive
- 关闭窗口时可选择最小化到托盘（**挂载保持**）或退出，可记住选择；托盘菜单「退出」时干净卸载（先冲刷上传）
- Windows 挂载参数内置 `uid=-1 gid=-1 umask=000`，普通用户身份即可创建/写文件（无需管理员权限）
- **自动更新**：基于 GitHub Releases，一键下载、安装并重启

数据位置：

| | Windows | macOS |
| --- | --- | --- |
| 配置（`servers.json`、`mounts.json`、`commands.json` 等） | `%APPDATA%\fileload\` | `~/Library/Application Support/fileload/` |
| 缓存 / 命令目录 / 日志 | `%LOCALAPPDATA%\fileload\{cache,bin,logs}` | `~/Library/Caches/fileload/{cache,bin,logs}` |

设置环境变量 `FILELOAD_HOME=<目录>` 可把以上数据全部放到指定目录（便携/测试模式）。

## 技术栈

- GUI：[Wails v3](https://v3.wails.io)（Go + 系统 WebView），界面为 `frontend/` 下的纯 HTML/CSS/JS，无需 Node 构建，直接嵌入程序
- 文件系统：[cgofuse](https://github.com/winfsp/cgofuse) + [WinFsp](https://winfsp.dev)（Windows）/ [FUSE-T](https://www.fuse-t.org)（macOS）
- SSH/SFTP：`golang.org/x/crypto/ssh` + `github.com/pkg/sftp`

## 构建

通用依赖：Go 1.26+，以及 CGO 编译器（cgofuse 需要）。以下命令都在**仓库根目录**执行，产物放在 `dist/`。

### macOS

需要 Xcode 命令行工具（`xcode-select --install`）。构建机**不需要**安装 FUSE-T：脚本会下载固定版本的 FUSE-T 安装包到 `build/darwin/vendor/`，用其中的头文件编译，并把安装包一起打进 app。

```bash
scripts/build_macos.sh                  # 本机架构，产出 dist/FileLoad.app 与 dist/FileLoad-<版本>-macos.dmg
ARCH=universal scripts/build_macos.sh   # arm64 + amd64 通用版
NODMG=1 scripts/build_macos.sh          # 只出 .app（开发调试更快）
```

### Windows

依赖：
- gcc + windres（如 scoop 安装的 MinGW）
- [WinFsp](https://winfsp.dev/rel/) 及其 fuse 头文件（编译期）。若 WinFsp 安装目录没有 `inc\fuse`，从 [winfsp 仓库](https://github.com/winfsp/winfsp/tree/master/inc/fuse) 下载 `fuse.h` `fuse_common.h` `fuse_opt.h` `winfsp_fuse.h` 到 `winfsp_inc\`
- 打安装包还需要 [Inno Setup 6](https://jrsoftware.org/isinfo.php)，以及放在 `dist\` 下的 `winfsp-2.1.msi`（从 WinFsp Releases 下载）

```powershell
$env:CPATH = "$PWD\winfsp_inc"

# 1) 生成图标 dist\fileload.ico（需要 Pillow）
python assets/make_ico.py

# 2) 生成 Windows 资源（exe 图标 + 版本信息）；syso 必须放在 main 包目录才会被链接
windres --codepage=65001 -i build/windows/app.rc -O coff -o cmd/fileload/rsrc_windows_amd64.syso --target=pe-x86-64

# 3) 构建
go build -trimpath -tags production -ldflags="-s -w -H windowsgui" -o dist/FileLoad.exe ./cmd/fileload

# 4) 打安装包，产出 dist\FileLoad-Setup-<版本>.exe
ISCC.exe build\windows\installer.iss
```

### 测试

```bash
go test ./...
```

`internal/vfs` 与 `internal/backend/sftp` 的集成测试需要真实 SSH 服务器，通过 `FILELOAD_IT_*` 环境变量启用（见 `internal/backend/sftp/client_test.go` 开头的说明）；macOS 上的真实挂载测试另需 `FILELOAD_FUSE=1` 且已安装 FUSE-T。未设置时自动跳过。

### 图标资源

- `assets/icon.svg` — 图标矢量源文件（1024×1024）
- `assets/icon_master.png` — 由 icon.svg 渲染的 1024 位图，macOS 的 `icon.icns` 由它生成
- `assets/make_ico.py` — 从 icon_master.png 生成 `dist/fileload.ico`（16/24/32/48/64/128/256）
- `assets/icon.png` — 程序内嵌的窗口图标（`assets.go` 用 `//go:embed` 引入）
- `assets/tray_template.svg` / `.png` — macOS 菜单栏模板图标，用 `swift scripts/make_tray_icon.swift` 由 svg 生成 png
- `build/windows/app.rc` — exe 图标 + VERSIONINFO，windres 编译成 syso

改图标：替换 `assets/icon.svg` 并重新渲染 `icon_master.png`，然后重新构建。

## 发版

版本号需同步修改三处：`internal/update/update.go`（`Version`，macOS 脚本从这里读取）、`build/windows/app.rc`、`build/windows/installer.iss`。

```bash
gh auth login                    # 一次
git tag v1.0.1 && git push origin main v1.0.1
python scripts/release.py publish 1.0.1 dist/FileLoad-Setup-1.0.1.exe dist/FileLoad-1.0.1-macos.dmg --notes notes.md
```

自动更新按资产名识别安装包：Windows 取名字含 `Setup` 的 `.exe`（以 `/SILENT` 运行，装完自动重启），macOS 取 `.dmg`（替换正在运行的 FileLoad.app 后自动重启，app 需位于「应用程序」等可写目录，不能直接在 dmg 里运行）。

可选：构建时设置环境变量 `FILELOAD_GH_TOKEN=github_pat_...`（只读令牌），`build_macos.sh` 会经 `-ldflags -X fileload/internal/update.EmbeddedToken=...` 把它内置到程序里，用户无需自己填令牌；Windows 构建在 `go build -ldflags` 里加同样的 `-X` 参数。内置令牌可被从二进制中提取，务必只授予本仓库 Contents 只读权限。

## 使用

1. 启动 FileLoad
2. 「目录挂载」页：已有挂载记录时直接点「挂载」一键恢复；否则在「新建挂载」里：
   - 第 1 步：填服务器信息（或下拉选已保存配置，启动时自动填入上次的服务器）→ 连接 → 自动进入第 2 步
   - 第 2 步：浏览远程目录，点文件夹进入，悬停点「选此目录」或底部「选择当前目录」→ 第 3 步
   - 第 3 步：选择盘符（Windows）或挂载目录（macOS）→ 挂载。之后像本地文件一样打开、用 VS Code/IDEA 编辑、本地解释器直接执行
3. 新开终端执行 `rcd` 或自定义命令：代理自动登录服务器、进入挂载目录后执行；「命令挂载」页可查看就绪状态并一键复制命令
4. 「关于」页：查看当前版本；发现新版本时点「立即更新」
5. 关窗时选择最小化到托盘（挂载保持）或退出；托盘菜单 → 退出（卸载并先冲刷上传）

## 已知限制

- 同一时间只有一个挂载；换目录需先卸载
- 大文件首次打开需等整文件下载完（无按需分块）
- 密码明文保存在本机配置目录的 `servers.json` 与 `mounts.json`（建议改用私钥登录）
- 强杀进程（任务管理器/断电）时未上传完的改动保留在本地缓存，下次挂载继续上传
- rcd 及自定义命令的免密依赖本地代理端口（`127.0.0.1:12222` 起），仅挂载期间有效
- rcd 及自定义命令的会话是交互式 shell，启动的程序（如 claude/codex）以 TUI 方式运行（与手动 SSH 一致），不是无人值守后台任务
- macOS 版使用的 FUSE-T 仅对非商业用途免费，商业使用请先查看其[许可条款](https://www.fuse-t.org)
