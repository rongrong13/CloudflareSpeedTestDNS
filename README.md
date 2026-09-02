# CloudflareSpeedLocalTest

> ⚠️ **免责声明**：本项目仅供学习与研究目的，使用者应自行遵守所在地区法律法规，作者不对使用本项目产生的任何后果承担责任。下载使用即视为已阅读并同意上述声明。

基于 [Lyxot/CloudflareSpeedTestDNS](https://github.com/Lyxot/CloudflareSpeedTestDNS) 的增强版本，在原项目基础上新增了 **Web 控制面板**、**Docker 一键部署**、**GitHub Gist 同步** 等功能。

## ✨ 新增特性（相比原项目）

- 🖥️ **Web 控制面板**：通过浏览器实时查看测速进度、结果、日志
- 🐳 **Docker 一键部署**：支持 `docker run` 和 `docker compose` 快速启动
- 📤 **GitHub Gist 同步**：测速结果自动上传到 Gist，支持增量更新（同一文件）
- ⏱ **定时测速**：Web 面板可配置 1~24 小时间隔自动测速
- 🔧 **在线参数调整**：延迟阈值、并发数、测速数量等可在 Web 面板实时调整
- 📊 **结果筛选排序**：支持按 IP、延迟、速度筛选，点击表头排序
- 🌓 **深色/浅色主题**：自动跟随系统，手动切换
- 📋 **一键复制/导出**：复制单个 IP、全部 IP、导出 CSV 文件
- 📜 **日志实时显示**：WebSocket 实时推送，支持自动滚动开关
- 📂 **结果历史**：浏览器本地保存最近 5 次测速结果

## 🚀 快速开始

### 方式一：Docker Compose（推荐）

```bash
# 克隆项目
git clone https://github.com/rongrong13/CloudflareSpeedLocalTest.git
cd CloudflareSpeedLocalTest

# 启动（可选：配置 Gist Token 以启用自动上传）
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
docker compose up -d

# 查看日志
docker compose logs -f
```

启动后访问 `http://你的IP:8080`

### 方式二：Docker 直接运行

```bash
# 克隆并构建
git clone https://github.com/rongrong13/CloudflareSpeedLocalTest.git
cd CloudflareSpeedLocalTest
docker build -t cloudflare-speed-localtest:latest .

# 运行
docker run -d \
  --name cfstd \
  --network host \
  --restart unless-stopped \
  -e GITHUB_TOKEN=ghp_xxxxxxxxxxxx \
  cloudflare-speed-localtest:latest
```

### 方式三：直接编译运行

```bash
# 编译
go build -ldflags "-s -w" -o cfstd .

# 运行 Web 模式（浏览器访问 http://localhost:8080）
./cfstd -web

# 运行命令行模式
./cfstd
```

## 🖥️ Web 控制面板功能

启动后访问 `http://你的IP:8080`：

- **开始/停止测速**：一键控制
- **参数调整**：最大延迟、最低速度、测速数量、并发数（上限100）、禁用下载
- **定时测速**：可设置 1~24 小时间隔自动测速
- **结果管理**：筛选、排序、复制、导出 CSV
- **Gist 同步**：一键上传或开启自动上传（始终更新同一文件）
- **历史记录**：保存最近 5 次测速结果（浏览器本地存储）
- **日志查看**：实时日志 + 自动滚动开关
- **主题切换**：深色/浅色/自动跟随系统

## ⚙️ 配置说明

可通过环境变量或 `config.toml` 配置文件调整参数：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-web` | false | 启动 Web 界面 |
| `-n` | 200 | 延迟测速并发数（Web 面板上限 100） |
| `-t` | 4 | 单 IP 延迟测速次数 |
| `-tp` | 443 | 测速端口 |
| `-tl` | 9999 | 平均延迟上限（ms） |
| `-tll` | 0 | 平均延迟下限（ms） |
| `-tlr` | 1.0 | 丢包率上限（0.00~1.00） |
| `-dn` | 10 | 下载测速数量 |
| `-dt` | 10 | 下载测速时间（秒） |
| `-sl` | 0 | 下载速度下限（MB/s） |
| `-dd` | false | 禁用下载测速 |
| `-p` | 10 | 显示结果数量 |

完整配置请参考 [config.example.toml](conf/config.example.toml)。

## 📤 GitHub Gist 同步

1. 创建 [GitHub Personal Access Token](https://github.com/settings/tokens)（勾选 `gist` 权限）
2. 启动时传入 Token：
   ```bash
   # Docker
   docker run -d -e GITHUB_TOKEN=ghp_xxx ...
   
   # 或 config.toml
   [gist]
   enable = true
   token = "ghp_xxxxxxxxxxxx"
   ```
3. Web 面板勾选「测速完成自动上传」，或手动点击「上传 Gist」

> Gist 文件固定为 `ips.txt`（纯文本，每行一个 IP），每次测速后更新同一文件而非创建新文件。

## 🙏 致谢

本项目基于以下开源项目：

- **[Lyxot/CloudflareSpeedTestDNS](https://github.com/Lyxot/CloudflareSpeedTestDNS)** - 原始项目
- **[XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)** - 核心测速引擎

## 📄 开源协议

本项目采用 GPL-3.0 开源协议，与原项目保持一致。

## 🔧 从源码编译

```bash
# 编译当前平台
go build -ldflags "-s -w" -o cfstd .

# 交叉编译 ARM64（适用于路由器）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o cfstd-arm64 .
```

## ⚠️ 免责声明

本项目出于学习和研究目的开发和使用，使用者应自行遵守所在地区的法律法规。作者不对因使用本项目而产生的任何直接或间接后果承担责任。请在下载后 24 小时内自行删除。
