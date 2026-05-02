# FasentNeo ⚡

跨平台局域网 / 远程文件高速传输工具。支持 Windows、Linux、Android。

## 特性

- **极速传输** — 1MB 缓冲区 + TCP_NODELAY，Linux 下自动零拷贝（sendfile）
- **自动发现** — UDP 广播自动扫描局域网内其他设备
- **远程直连** — 配合内网穿透（FRP/Ngrok 等）可直接跨公网传输
- **拖拽即传** — 浏览器 Web 界面，支持拖拽选择文件
- **多文件并发** — 多文件并行传输，实时进度显示
- **免依赖** — 单个二进制文件，无运行时依赖

## 快速开始

### 下载

从 [Releases](https://github.com/TTHILLTT/FasentNeo/releases) 下载对应平台的二进制：

| 平台 | 文件 |
|------|------|
| Windows 64-bit | `fasentneo-windows-amd64.exe` |
| Linux 64-bit | `fasentneo-linux-amd64` |
| Linux ARM64 | `fasentneo-linux-arm64` |

### 使用

1. 在两台设备上运行 `fasentneo`
2. 浏览器会自动打开 Web 界面
3. 在设备列表中选择目标设备
4. 拖拽文件到上传区，点击发送
5. 接收文件默认保存在 `~/Downloads/FasentNeo/`

### 远程设备（公网传输）

如果双方设备已通过 FRP / Ngrok 等工具配置好内网穿透：

1. 启动 FasentNeo，页面顶部会显示本机传输地址
2. 在对方设备的 Web UI 中输入公网地址，点击「添加」
3. 添加后即可像局域网一样发送文件，传输走 TCP 直连

## 从源码编译

```bash
# 需要 Go 1.21+
git clone https://github.com/TTHILLTT/FasentNeo.git
cd FasentNeo

# 仅编译当前平台
go build -o fasentneo .

# 交叉编译所有平台
.\build.ps1 -Target all   # Windows
make all                  # Linux / macOS
```text

## 技术架构

```text
  浏览器 Web UI (拖拽 · 设备列表 · 进度)
        │
   Go HTTP Server (:8080)
   API 路由 · SSE 事件推送
   ┌─────────┴──────────┐
   UDP Discovery        TCP Transfer
   端口 54321           端口 54322
   广播心跳 3s          1MB buf · NODELAY
   设备过期 10s         sendfile 零拷贝
```

| 端口 | 协议 | 用途 |
|------|------|------|
| 8080 | TCP | Web UI + REST API |
| 54321 | UDP | 局域网设备发现 |
| 54322 | TCP | 文件数据传输 |

## 协议

MIT License — 详见 [LICENSE](LICENSE)
