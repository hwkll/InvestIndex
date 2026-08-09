# InvestHub · 个人综合投资管理平台

> v1.2.0 · 把加密货币、基金、黄金、股票放进同一个视图，本地跑、数据不出机器、单文件部署。

InvestHub 是一个**纯本地、自托管**的个人投资管理平台。它把分散在各 App 里的持仓统一到一个仪表盘，自动拉行情、算浮盈、跑技术指标、触发提醒，并可选接入 DeepSeek 做 AI 研判。

**整个应用编译成一个约 17 MB 的可执行文件** —— 前端资源通过 `go:embed` 嵌进二进制，数据库是零 CGO 的纯 Go SQLite（cgo-free）。没有 Docker，没有 Nginx，没有 `node_modules` 要部署，拷贝一个文件到任何 macOS / Linux / Windows 机器上即可运行。

---

## 目录

- [核心特性](#核心特性)
- [隐私优先](#隐私优先)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [从源码构建](#从源码构建)
- [配置项](#配置项)
- [项目结构](#项目结构)
- [功能详解](#功能详解)
- [API 参考](#api-参考)
- [实时推送（SSE）](#实时推送sse)
- [数据模型](#数据模型)
- [安全模型](#安全模型)
- [数据备份与迁移](#数据备份与迁移)
- [开发指南](#开发指南)
- [常见问题](#常见问题)
- [许可](#许可)

---

## 核心特性

| 模块 | 能力 |
|------|------|
| **仪表盘** | 总资产 / 浮动盈亏 / 今日盈亏 / 现金占比 四大 KPI（加载时**脉冲骨架屏**占位）；资产分布环形图；7d / 30d / 90d / 全部 资产趋势折线图（支持**基准对比叠加**）；分类概览表（**实时增量刷新**，含今日最强/最弱）；**行情状态徽标**（真实 / 模拟 / 陈旧）；**页面切换淡入淡出过渡** |
| **持仓管理** | 加密货币 / 基金 / 黄金 / 股票 四大分类（`subType` 可再标记 ETF 等二级类型）；移动加权平均成本法计算持仓成本、浮盈、收益率；行情经 SSE 实时跳动，前端客户端按汇率重算 |
| **自选 / 关注列表** | 从持仓页 / 资产详情页一键「加自选」；独立自选页管理，支持目标价与备注，实时显示最新价、涨跌幅、行情状态 |
| **资产详情** | ECharts K 线（日 / 周 / 月）+ MA5 / MA20 / MA60 + 成交量副图 + 区间缩放；MACD、RSI、KDJ、布林带等技术指标 KPI |
| **交易流水** | 买入 / 卖出 双向记录（数量、价格、手续费、备注）；支持筛选、分页、编辑、删除；CSV 双向导入导出 |
| **AI 研判** | 接入 DeepSeek 对单个标的或整体组合做分析，输出信号（买入 / 卖出 / 观望）、理由、风险与建议动作；无 Key 时自动降级为本地启发式规则引擎 |
| **智能提醒** | 5 类规则：价格阈值、涨跌幅、区间突破、AI 信号、定时播报；多渠道（**站内 + 邮件 + Webhook**）；5 分钟防抖；通知中心 + 未读角标 |
| **多币种汇率** | `fx_rates` 表维护各币种对 CNY 汇率，经 CNY 枢纽统一换算；设置页可增删币种与编辑汇率 |
| **现金账户** | 多币种现金账户，按汇率折算主币种并计入总资产 |
| **基准对比 & 收益归因** | 以任意标的作为基准，仪表盘趋势图叠加归一化曲线，计算组合**超额收益（Excess Return）** |
| **数据自治** | 全量 JSON 备份 / 恢复；交易流水 CSV 导入导出（UTF-8 BOM，Excel 直接打开不乱码） |

---

## 隐私优先

- 所有数据存在本机一个 SQLite 文件里，**不上传任何服务器**
- API Key 等敏感配置用 **AES-256-GCM 加密**后落盘，主密钥单独存放且权限 `0600`
- 唯一的出网请求是你主动开启的行情源（Binance / 新浪财经）和可选的 DeepSeek API
- 断网可用：行情源不可达时自动降级到内置模拟器，功能不中断

---

## 技术栈

### 后端

| 组件 | 选型 | 说明 |
|------|------|------|
| 语言 | **Go 1.25** | 单二进制、跨平台交叉编译 |
| 路由 | `go-chi/chi v5`（v5.3.1） | 轻量、标准库兼容 |
| 数据库 | `modernc.org/sqlite`（v1.56.0） | **纯 Go 实现，零 CGO** —— 这是能出单文件的关键 |
| 加密 | `golang.org/x/crypto`（v0.54.0） | scrypt 口令哈希 + AES-256-GCM 配置加密 |
| 资源嵌入 | `go:embed` | 前端产物编译进二进制 |
| 唯一 ID | `github.com/google/uuid` | 会话、提醒事件、分析记录等 |

### 前端

| 组件 | 选型 |
|------|------|
| 框架 | **Vue 3.5**（Composition API + `<script setup>`） |
| 路由 | vue-router 4 |
| 状态 | Pinia 2 |
| 图表 | **ECharts 5**（按需 tree-shaking，只打包用到的图表与组件） |
| 构建 | Vite 5 |

> 前端仅在**开发 / 构建阶段**需要 Node.js（18+）。最终交付物不含任何 JS 运行时依赖。

---

## 快速开始

### 直接运行（已有二进制）

```bash
./investhub
```

打开 <http://localhost:7788> 即可。

首次启动会自动完成：

1. 在 `./data/investhub.db` 创建数据库并建表（含 `fx_rates`、`watchlist` 等共 16 张表）
2. 生成加密主密钥 `./data/master.key`（权限 0600）
3. 播种演示数据：BTC、ETH、贵州茅台（`sh.600519`）、沪深300ETF（`510300`）、黄金ETF（`518880`）+ 5 笔买入 + 2 个现金账户（招商银行卡 ¥80,000 + 华泰证券账户 ¥30,000 = ¥110,000）
4. 播种汇率：CNY = 1、USD = 7.2、HKD = 0.92
5. 回填 90 天历史快照，让趋势图立刻有内容
6. 启动行情轮询调度器（默认每 30 秒一轮，经 SSE 推送到前端）

**默认无需登录**。想开启鉴权，去「设置」页设置一个 6 位以上访问口令即可。

### 自定义端口与数据位置

```bash
PORT=8080 INVESTHUB_DB=/Users/me/invest/hub.db ./investhub
```

### 后台运行与关闭

InvestHub 是前台进程——终端关掉它就停了。想长期挂着，按你的系统选一种方式：

**macOS / Linux — 后台运行**

```bash
# 启动（日志写入文件，进程放后台）
nohup ./investhub > investhub.log 2>&1 &

# 查看日志
tail -f investhub.log

# 关闭：先找到进程 ID
ps aux | grep investhub | grep -v grep
# 然后优雅终止（发送 SIGTERM，服务会等当前请求处理完再退出）
kill <PID>

# 或者一步到位：
pkill -f './investhub'
```

**macOS — 开机自启（launchd）**

创建 `~/Library/LaunchAgents/com.investhub.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.investhub</string>
  <key>ProgramArguments</key>
  <array>
    <string>/path/to/investhub</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/path/to/investhub-dir</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/path/to/investhub-dir/investhub.log</string>
  <key>StandardErrorPath</key>
  <string>/path/to/investhub-dir/investhub.log</string>
</dict>
</plist>
```

加载与启停：

```bash
launchctl load ~/Library/LaunchAgents/com.investhub.plist   # 加载（开机自启）
launchctl start com.investhub                                # 手动启动
launchctl stop com.investhub                                 # 停止
launchctl unload ~/Library/LaunchAgents/com.investhub.plist  # 卸载（取消自启）
```

**Linux — systemd 服务**

创建 `/etc/systemd/system/investhub.service`：

```ini
[Unit]
Description=InvestHub 个人投资管理
After=network.target

[Service]
Type=simple
User=your-user
WorkingDirectory=/path/to/investhub-dir
ExecStart=/path/to/investhub
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

启停：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now investhub    # 启动 + 开机自启
sudo systemctl stop investhub            # 停止
sudo systemctl disable investhub         # 取消自启
journalctl -u investhub -f               # 查看日志
```

**Windows — 后台运行**

```powershell
# 启动（PowerShell，最小化窗口）
Start-Process -WindowStyle Hidden -FilePath ".\investhub.exe"

# 关闭：在任务管理器找到 investhub.exe 结束进程，
# 或者在 PowerShell 中：
Get-Process investhub | Stop-Process
```

> 💡 优雅关闭说明：进程收到 `SIGTERM`（Linux/macOS 的 `kill`、systemd 的 `stop`）或 `Ctrl+C` 时，InvestHub 会等待当前 HTTP 请求完成后关闭调度器、清理连接，约 1–3 秒退出。`kill -9` / `SIGKILL` 会立即终止，下次启动无影响（SQLite WAL 自动恢复）。

### 重置为全新状态

```bash
rm -rf data/          # 清空数据库和密钥，下次启动重新播种演示数据
```

---

## 从源码构建

### 前置要求

| 工具 | 版本 | 用途 |
|------|------|------|
| **Go** | 1.25+ | 后端编译，**全平台通用** |
| **Node.js** | 18+ | 仅构建前端（日常开发可选，仓库已含预构建 dist） |

> Go 1.25 可以跨平台编译出 macOS / Linux / Windows 的二进制，无需安装额外 SDK。

---

### 构建步骤

前端只需要构建一次（产物在 `internal/web/dist/`），Go 编译会自动嵌入它。后续只改 Go 代码时可跳过前端步骤直接 `go build`。

#### 第一步：构建前端（跨平台通用）

```bash
cd frontend
npm install
npm run build
cd ..
```

> 这一步在 **macOS / Linux / Windows** 上完全相同。产物输出到 `internal/web/dist/`。

#### 第二步：编译 Go → 单二进制

按你的操作系统选一条：

**macOS（Intel 芯片）**

```bash
go build -o investhub ./cmd/investhub
```

**macOS（Apple Silicon M1/M2/M3）**

```bash
GOARCH=arm64 go build -o investhub ./cmd/investhub
```

> Go 1.25 默认会编译出适配当前机器的架构，上面的 `GOARCH=arm64` 通常可省略。

**Linux（amd64）**

```bash
go build -o investhub ./cmd/investhub
```

**Windows（amd64）**

```powershell
go build -o investhub.exe ./cmd/investhub
```

> Windows 上二进制文件名必须带 `.exe` 后缀，否则无法直接运行。

编译产物是**一个单文件**（约 17 MB），前端所有 JS/CSS/HTML 都嵌在里面，无需任何外部依赖。

#### 第三步：运行

| 平台 | 命令 |
|------|------|
| macOS / Linux | `./investhub` |
| Windows（PowerShell） | `.\investhub.exe` |
| Windows（CMD） | `investhub.exe` |

打开 <http://localhost:7788>。

---

### 从一台机器编译给多平台用（交叉编译）

Go 的 SQLite 驱动是纯 Go 实现，交叉编译**不需要任何 C 工具链**。在任意一台机器上就能产出三个平台的文件：

```bash
# 在任何一台机器上（macOS / Linux / Windows）执行：
GOOS=darwin  GOARCH=amd64 go build -o investhub-mac-intel   ./cmd/investhub
GOOS=darwin  GOARCH=arm64 go build -o investhub-mac-silicon ./cmd/investhub
GOOS=linux   GOARCH=amd64 go build -o investhub-linux       ./cmd/investhub
GOOS=windows GOARCH=amd64 go build -o investhub.exe          ./cmd/investhub
```

| 产物 | 目标平台 |
|------|---------|
| `investhub-mac-intel` | macOS（Intel 芯片） |
| `investhub-mac-silicon` | macOS（Apple Silicon M1+） |
| `investhub-linux` | Linux amd64（Debian / Ubuntu / CentOS 等） |
| `investhub.exe` | Windows 10 / 11 amd64 |

> `GOARCH=arm64` 的 Linux 编译（树莓派、AWS Graviton）：`GOOS=linux GOARCH=arm64 go build -o investhub-linux-arm64 ./cmd/investhub`

### 体积优化

```bash
go build -ldflags="-s -w" -o investhub ./cmd/investhub    # 约 17MB → 12MB
```

> ⚠️ **注意**：如果跳过第一步直接 `go build`，二进制能编译成功（`internal/web/dist/.gitkeep` 占位），但访问首页会是空白 —— 因为没有前端资源可嵌入。仓库里已包含预构建的 dist，通常不需要关注这个问题。

---

## 配置项

### 环境变量（启动时读取）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `7788` | HTTP 监听端口 |
| `HOST` | *(空)* | 监听地址。留空等价于 `:PORT`，即**监听所有网卡**（局域网可访问）。设为 `127.0.0.1` 可限制为仅本机 |
| `INVESTHUB_DB` | `./data/investhub.db` | SQLite 数据库路径，主密钥存放于同目录 |
| `INVESTHUB_SECRET` | *(空)* | 自定义加密主密钥；留空则使用 `data/master.key`（自动创建） |
| `POLL_INTERVAL` | `30` | 行情轮询间隔（秒），上限 600（10 分钟），防止错失每日快照 |
| `INVESTHUB_WEB_DIR` | *(空)* | 指定后从该磁盘目录读取前端资源，**绕过 `go:embed`**，改前端不用重编译 Go —— 开发时很有用 |

### 应用内设置（「设置」页，存数据库）

| 键 | 默认值 | 说明 |
|----|--------|------|
| `currency` | `CNY` | 主计价币种，所有外币资产按汇率折算到此币种 |
| `data_source_mode` | `auto` | 行情数据源模式：<br>`auto` = 优先真实源，失败自动降级模拟器<br>`real` = 只用真实源，失败即报错<br>`sim` = 强制模拟器（离线演示 / 截图用） |
| `rate_usd_cny` | `7.2` | **已弃用**：旧版单汇率设置，仅作为后端兜底（`fx_rates` 表中 USD 行优先）。设置页已改为「多币种汇率」表 |
| `deepseek_api_key` | *(空)* | DeepSeek API Key，**AES-256-GCM 加密存储**；接口只返回 `{has_value: true/false}`，永不回传明文 |
| `deepseek_model` | `deepseek-chat` | 使用的模型名 |
| `webhook_url` | *(空)* | 提醒 Webhook 地址（任意兼容 JSON POST 的服务，如企业微信 / 钉钉 / 自定义），提醒渠道含 `webhook` 时触发 |
| `benchmark` | *(空)* | 基准对比标的 `symbol`（如 `BTC`）；设置后仪表盘趋势图叠加其归一化曲线并计算超额收益 |
| `smtp_*` | *(空)* | 邮件提醒配置：`smtp_host` / `smtp_port` / `smtp_from` / `smtp_to` / `smtp_user` / `smtp_pass` / `smtp_tls`（`smtp_pass` 加密存储） |

### 多币种汇率（`fx_rates` 表，非设置键）

| 字段 | 含义 |
|------|------|
| `currency` | 币种代码（如 `USD`、`HKD`、`EUR`） |
| `rate` | **1 单位该币种 = `rate` 个 CNY**（CNY 固定为 1，作为换算枢纽） |

通过 `GET/PUT /settings/fx` 读写。未知币种换算回退：优先用 USD 汇率，否则按 1 处理。

### 行情数据源

| 类型 | 数据源 | 标识 | 计价 |
|------|--------|------|------|
| 加密货币 | Binance 公开行情接口（24h ticker，无需 key） | `binance` | USD |
| A 股 / 场内 ETF | 新浪财经行情接口 | `sina` | CNY |
| 现货黄金 | 新浪 `gds_AUTD`（上海金交所 Au(T+D)，CNY/克） | `sge` | CNY |
| 黄金 ETF（场内） | 新浪财经行情接口 | `sina` | CNY |
| 场外开放式基金 | 天天基金净值（东方财富；当前部署环境不可达，拉取失败时诚实标注 `nosource`，不会编造价格） | `fund_eastmoney` | CNY |
| 全部 | 内置随机游走模拟器（真实源不可用时的兜底） | `sim` | — |

行情带内存缓存和 K 线缓存，避免重复请求打爆上游。每条行情携带 `status`：`ok`（真实源）/ `sim`（模拟器）/ `stale`（真实源但数据偏旧）。

---

## 项目结构

```
InvestIndex/
├── cmd/investhub/
│   └── main.go                 # 入口：初始化 → 启动调度器 → 监听端口 → 优雅关闭
│
├── internal/                   # 后端分层
│   ├── store/       store.go   # SQLite 连接、16 张表建表 DDL、查询辅助、seed
│   ├── cryptox/               # 主密钥管理、AES-256-GCM 加解密、scrypt 哈希
│   ├── settings/   settings.go # KV 配置，敏感键自动加密
│   ├── quotes/     quotes.go   # 行情抓取（Binance / 新浪）、模拟器、缓存、K 线
│   ├── indicators/             # MA / MACD / RSI / KDJ / BOLL 等技术指标计算
│   ├── core/                   # 核心业务：资产、交易、持仓成本、盈亏、快照、现金
│   │   ├── core.go             #   资产 / 持仓 / 汇总 / 基准对比 / FX 换算
│   │   └── watchlist.go        #   自选列表（ListWatchlist / Add / Update / Remove）
│   ├── ai/             ai.go   # DeepSeek 调用 + 本地启发式兜底 + 并发限流
│   ├── alerts/     alerts.go   # 提醒规则引擎、渠道分发（站内/邮件/Webhook）、防抖
│   ├── api/                    # HTTP 层
│   │   ├── api.go              #   路由表、CORS、鉴权中间件、统一响应封装
│   │   ├── handlers.go         #   各业务 handler
│   │   ├── sse.go              #   SSE Hub：多客户端广播、心跳
│   │   ├── scheduler.go        #   后台轮询、快照、演示数据播种（SeedDemo）
│   │   ├── backup.go           #   JSON 全量备份 / CSV 导入导出
│   │   └── static.go           #   静态资源与 SPA 路由回退
│   └── web/
│       ├── web.go              # go:embed all:dist
│       └── dist/               # 前端构建产物（由 vite 生成）
│
├── frontend/                   # Vue 3 单页应用（10 条路由：登录 + 9 个页面）
│   ├── src/
│   │   ├── main.js             # 应用引导 + 路由守卫
│   │   ├── App.vue             # 侧边栏导航 + router-view
│   │   ├── router.js           # 10 条路由
│   │   ├── store.js            # Pinia：登录态、SSE 连接、实时行情合并、Toast
│   │   ├── api.js              # REST 封装，统一解包 {code,data,msg}
│   │   ├── echarts.js          # ECharts 按需注册（tree-shaking）
│   │   ├── format.js           # 金额 / 百分比 / 日期格式化、配色常量
│   │   ├── styles.css          # 全局样式
│   │   ├── components/         # EChart（含空状态）/ ModalDialog（含背景锁）/ StatCard（含骨架屏）/ ToastHost
│   │   └── views/              # 9 个页面视图
│   │       ├── DashboardView.vue   # 实时增量合并 + 基准对比 + 行情状态徽标
│   │       ├── PositionsView.vue   # 持仓 + 「加自选」按钮
│   │       ├── AssetDetailView.vue # 资产详情 + K线/指标 + 「加自选」
│   │       ├── TransactionsView.vue
│   │       ├── AiView.vue
│   │       ├── AlertsView.vue       # 提醒规则（含 Webhook 渠道）
│   │       ├── CashView.vue
│   │       ├── WatchlistView.vue    # 自选列表
│   │       ├── SettingsView.vue     # 多币种汇率 / Webhook / 基准 / 通用 / SMTP
│   │       └── LoginView.vue
│   ├── vite.config.js          # 产物输出到 ../internal/web/dist，dev 代理到 :7788
│   ├── package.json
│   └── package-lock.json       # 锁定依赖版本；node_modules 未随仓库保存，需 npm install
│
├── data/                       # 运行时数据（首次启动自动创建，已 gitignore）
│   ├── investhub.db            #   SQLite 数据库
│   └── master.key              #   加密主密钥，权限 0600
│                               #   仓库中不存在此目录，删掉即可重置为全新状态
│
├── go.mod / go.sum
└── investhub                   # 编译产物（已 gitignore）
```

---

## 功能详解

### 持仓成本核算

采用**移动加权平均成本法**：

- 买入：`新均价 = (原持仓成本 + 本次买入金额 + 手续费) / (原数量 + 本次数量)`
- 卖出：均价不变，扣减数量，差额计入**已实现盈亏**
- 浮动盈亏：`(现价 - 均价) × 持仓数量`
- 外币资产按 `fx_rates` 经 CNY 枢纽折算到主币种后再汇总

### 自选 / 关注列表

- 在「持仓」页或「资产详情」页点击「加自选」即可加入；「自选」页集中管理
- 每条自选可设**目标价**（`targetPrice`）与备注（`note`）
- 列表实时显示当前价、涨跌幅、行情状态；越过目标价可在「提醒」中配置价格规则联动

### 多币种汇率换算

- `fx_rates` 表以 **CNY 为枢纽**：`Convert(amount, from, to) = amount × rate(from) ÷ rate(to)`
- 设置页「多币种汇率」卡片可增删币种、编辑汇率（独立保存，落 `fx_rates` 表）
- 旧版单体 `rate_usd_cny` 仅作后端兜底，UI 已不再暴露

### 基准对比 & 收益归因

- 在「设置」或「仪表盘」将 `benchmark` 设为某标的 `symbol`（如 `BTC`）
- 趋势图叠加该标的归一化到 100 的曲线；`GET /pnl/trend` 返回：
  - `benchmarkLabel`：基准标的
  - `benchmark[]`：基准时间序列（归一化）
  - `excessReturn`：**组合超额收益** = 组合区间收益 − 基准区间收益（百分点）
- 设置页与仪表盘共用同一 `benchmark` 键，保持联动一致

### 提醒规则

| 类型 | 触发条件 | 关键参数 |
|------|----------|----------|
| `price` | 现价突破指定阈值 | 方向（上穿 / 下穿）、阈值 |
| `percent` | 涨跌幅超过阈值 | 方向、百分比阈值 |
| `range_break` | 突破近 N 日最高 / 最低价 | 回看天数 N |
| `ai_signal` | AI 研判给出买入 / 卖出信号 | 绑定标的 |
| `schedule` | 每日定时播报 | 时间 `HH:MM`（默认 09:00） |

**渠道**：`web`（站内）、`mail`（邮件，需配置 SMTP）、`webhook`（外部 HTTP 回调，需配置 `webhook_url`）。可组合，如 `web,mail,webhook`。

同一规则 **5 分钟内不重复触发**（防抖）。触发后写入事件表，前端经 SSE 收到 `alert` 事件并更新未读角标。

### AI 研判降级策略

1. 配置了 `deepseek_api_key` → 调用 DeepSeek，最多 2 个并发
2. 未配置 / 调用失败 → 自动切换**本地启发式引擎**，基于 RSI、MACD、均线位置、涨跌幅等指标给出规则化结论

两种模式的输出结构完全一致，前端无感知。

### 行情状态透明度

每条行情带 `status` 字段：`ok`（来自真实源）/ `sim`（来自内置模拟器）/ `stale`（真实源但数据偏旧）。仪表盘顶部在存在 `sim` / `stale` 资产时显示提示徽标，让用户清楚当前数据的可信度。

---

## API 参考

所有接口以 `/api/v1` 为前缀（SSE 除外），统一响应封装：

```jsonc
{ "code": 0, "data": { /* ... */ }, "msg": "ok" }   // 成功：code === 0
{ "code": 40401, "data": null, "msg": "接口不存在" }  // 失败：code !== 0
```

### 认证

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/auth/status` | 返回 `{authRequired, loggedIn, version}` |
| `POST` | `/auth/login` | 口令登录，下发 HttpOnly 会话 Cookie |
| `POST` | `/auth/logout` | 注销当前会话 |
| `PUT` | `/auth/pin` | 设置 / 修改 / 清除访问口令（改口令需带旧口令） |

### 仪表盘

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/dashboard/summary` | 全局汇总（KPI + 分类明细 + 今日最强/最弱 `top` + `quoteSimCount` / `quoteStaleCount`） |
| `GET` | `/pnl/trend?range=7d\|30d\|90d\|all` | 资产趋势时间序列；含 `benchmarkLabel` / `benchmark[]` / `excessReturn` |

### 资产与持仓

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/assets` | 资产列表（含实时行情与健康状态） |
| `POST` | `/assets` | 新增标的 |
| `PUT` | `/assets/{id}` | 修改标的 |
| `DELETE` | `/assets/{id}` | 删除标的 |
| `GET` | `/assets/{id}/quote` | 单标的最新行情 |
| `GET` | `/assets/{id}/kline?period=1d&limit=120` | K 线数据 |
| `GET` | `/assets/{id}/indicators` | 技术指标（MA / MACD / RSI / KDJ / BOLL） |
| `GET` | `/assets/{id}/position` | 单标的持仓视图 |
| `GET` | `/positions?category=crypto` | 指定分类持仓（不传则返回全部分类映射） |

### 自选 / 关注列表

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/watchlist` | 自选列表（JOIN 资产 + 合并实时行情） |
| `POST` | `/watchlist` | 新增自选，body：`{assetId, targetPrice?, note?}`（按 assetId 去重） |
| `PUT` | `/watchlist/{id}` | 修改自选，body：`{targetPrice?, note?}` |
| `DELETE` | `/watchlist/{id}` | 删除自选 |

### 交易流水

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/transactions` | 分页列表，支持按标的 / 方向 / 时间筛选 |
| `POST` | `/transactions` | 新增交易 |
| `PUT` | `/transactions/{id}` | 修改交易 |
| `DELETE` | `/transactions/{id}` | 删除交易 |

### AI 研判

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/ai/analyze` | 触发分析，body：`{scope:"asset"\|"portfolio", assetId?}` |
| `GET` | `/ai/analyses` | 历史分析列表 |
| `GET` | `/ai/analyses/{id}` | 分析详情 |
| `DELETE` | `/ai/analyses/{id}` | 删除记录 |

### 提醒

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` / `POST` | `/alerts` | 规则列表 / 新建规则（channel 可含 `webhook`） |
| `PUT` / `DELETE` | `/alerts/{id}` | 修改 / 删除规则 |
| `GET` | `/alerts/events` | 触发记录 |
| `POST` | `/alerts/events/{id}/read` | 标记已读 |

### 设置

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` / `PUT` | `/settings` | 读取（敏感值脱敏）/ 更新配置（仅接受白名单内已知键，静默跳过未知键） |
| `POST` | `/settings/ai-test` | 测试 DeepSeek 连通性 |
| `POST` | `/settings/mail-test` | 测试 SMTP 连通性 |
| `POST` | `/settings/webhook-test` | 发送测试 Webhook 消息（校验 `webhook_url` 连通性） |
| `GET` / `PUT` | `/settings/fx` | 读取 / 批量更新 `fx_rates`（跳过 rate ≤ 0） |

### 现金账户

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` / `POST` | `/cash-accounts` | 现金账户列表 / 新建 |
| `PUT` / `DELETE` | `/cash-accounts/{id}` | 修改 / 删除 |

### 数据备份

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/data/export` | 全量 JSON 备份下载 |
| `POST` | `/data/import` | JSON 备份恢复 |
| `GET` | `/data/export.csv` | 交易流水 CSV 导出（UTF-8 BOM） |
| `POST` | `/data/import.csv` | 交易流水 CSV 导入 |

### 示例

```bash
# 查看总览（含模拟/陈旧行情计数）
curl -s http://localhost:7788/api/v1/dashboard/summary | jq .data

# 触发组合 AI 分析
curl -s -X POST http://localhost:7788/api/v1/ai/analyze \
     -H 'Content-Type: application/json' \
     -d '{"scope":"portfolio"}' | jq .data

# 记一笔买入
curl -s -X POST http://localhost:7788/api/v1/transactions \
     -H 'Content-Type: application/json' \
     -d '{"assetId":"<ASSET_ID>","direction":"buy","quantity":0.1,
          "price":60000,"fee":1,"tradeTime":1785995244885}'

# 把 BTC 设为基准，查看超额收益
curl -s -X PUT http://localhost:7788/api/v1/settings \
     -H 'Content-Type: application/json' -d '{"benchmark":"BTC"}'
curl -s "http://localhost:7788/api/v1/pnl/trend?range=30d" | jq .data.excessReturn

# 更新多币种汇率
curl -s -X PUT http://localhost:7788/api/v1/settings/fx \
     -H 'Content-Type: application/json' -d '{"CNY":1,"USD":7.1,"HKD":0.90,"EUR":7.8}'

# 加自选
curl -s -X POST http://localhost:7788/api/v1/watchlist \
     -H 'Content-Type: application/json' -d '{"assetId":"<ASSET_ID>","targetPrice":3000}'

# 备份全部数据
curl -s http://localhost:7788/api/v1/data/export -o investhub-backup.json
```

---

## 实时推送（SSE）

前端通过 `EventSource` 连接 `GET /api/v1/events` 接收服务端推送（该端点**不在** JSON 信封内）。

| 事件 | 触发时机 | 载荷 |
|------|----------|------|
| `quote` | 每轮行情轮询完成（默认 30s） | 单条行情 `{assetId, price, prevClose, chgPct, currency, sourceTime, status}`（`status` ∈ `ok`/`sim`/`stale`） |
| `alert` | 提醒规则命中 | 提醒事件对象 |
| `ai_done` | AI 分析完成 | 分析结果对象 |

连接建立即发送 `: connected`，之后每 **25 秒**发一次 `: ping` 心跳保活（防止反向代理掐断空闲连接）。广播采用**非阻塞写**，慢客户端不会拖垮整个推送。

仪表盘利用 `quote` 事件做**实时增量合并**：缓存各分类 positions，每收到一个 tick 仅用实时价 + 客户端汇率重算分类表，避免每轮全量 reload；只有 30s 定时器才触发全量 `loadAll`。

```bash
# 手动观察推送流
curl -N http://localhost:7788/api/v1/events
```

---

## 数据模型

SQLite 共 **16 张表**：

| 表 | 用途 |
|----|------|
| `meta` | schema 版本、是否已完成引导、是否为演示数据等元信息 |
| `settings` | 配置项（敏感值密文存储） |
| `sessions` | 登录会话 |
| `asset_categories` | 资产分类字典（crypto 加密货币 / fund 基金 / gold 黄金 / stock 股票） |
| `assets` | 资产标的（代码、名称、分类、币种、二级类型、数据源） |
| `transactions` | 交易流水，`direction` 受 `CHECK(direction IN ('buy','sell'))` 约束 |
| `position_snapshots` | 每日持仓快照，趋势图数据源 |
| `price_snapshots` | 每日收盘价快照 |
| `cash_accounts` | 现金账户 |
| `cash_snapshots` | 每日现金快照 |
| `kline_cache` | K 线缓存，减少上游请求 |
| `alert_rules` | 提醒规则 |
| `alert_events` | 提醒触发记录 |
| `ai_analyses` | AI 分析历史 |
| `fx_rates` | 多币种汇率（currency → rate，1 单位该币 = rate 个 CNY） |
| `watchlist` | 自选列表（asset_id 外键 + 目标价 + 备注 + 排序） |

---

## 安全模型

| 环节 | 做法 |
|------|------|
| 访问口令 | **scrypt** 哈希后存储，永不明文落盘 |
| 敏感配置 | **AES-256-GCM** 加密，主密钥 `data/master.key` 权限 `0600`（可用 `INVESTHUB_SECRET` 覆盖） |
| 会话 | HttpOnly Cookie，30 天**滑动过期**（SSE 长连接不触发续期），登出即失效；清除口令会吊销全部会话 |
| 暴力破解 | 连续 **5 次失败锁定 5 分钟**（按客户端 IP 计） |
| 请求体 | 限制 **20 MB**，防止内存打爆 |
| 设置更新 | 仅接受白名单内 14 个已知键，静默跳过未知 key |
| 备份导入 | JSON 列名经 `^[a-zA-Z_][a-zA-Z0-9_]*$` 正则校验，防 SQL 注入 |
| 监听地址 | 默认 `:7788`，即**所有网卡**（同一局域网内可访问） |

> ⚠️ **重要**：默认监听地址是 `:7788` 而非 `127.0.0.1:7788`，同一 Wi-Fi 下的其他设备能访问到你的投资数据。两种收敛方式：
>
> ```bash
> HOST=127.0.0.1 ./investhub      # 方式一：只允许本机访问
> ```
>
> 方式二：在「设置」页设置访问口令，开启鉴权（未登录只能看到登录页）。
>
> 如果这台机器在公共网络上，**两者建议都做**。

---

## 数据备份与迁移

### 全量备份

「设置」页点「导出 JSON」，或：

```bash
curl -s http://localhost:7788/api/v1/data/export -o backup-$(date +%F).json
```

导出业务表（不含 `sessions` 和 `meta`），恢复时在事务中用 `INSERT OR REPLACE` 写回，可安全重复执行。

### 换机迁移

最简单的方式：**直接拷贝 `data/` 整个目录**（数据库 + 主密钥）到新机器同路径即可。

> ⚠️ 只拷 `.db` 不拷 `master.key`，加密过的 API Key 将无法解密（其他数据不受影响，重填 Key 即可）。

### 交易流水 CSV

CSV 使用 UTF-8 BOM 编码 + 中文表头，Excel / Numbers 直接打开不乱码，可批量编辑后再导入。

---

## 开发指南

### 前后端分离热更新

开两个终端：

```bash
# 终端 1：后端
go run ./cmd/investhub

# 终端 2：前端（Vite dev server，已配置 /api 代理到 :7788）
cd frontend && npm install && npm run dev
```

访问 Vite 给出的地址（通常 <http://localhost:5173>），改 `.vue` 文件即时热更新。

### 只改前端、不想重编译 Go

```bash
cd frontend && npm run build
INVESTHUB_WEB_DIR=./internal/web/dist ./investhub
```

设了 `INVESTHUB_WEB_DIR` 后，Go 从磁盘目录读前端资源而非嵌入的副本，改完 `npm run build` 刷新浏览器即可。

### 加快调试节奏

```bash
POLL_INTERVAL=5 INVESTHUB_DB=/tmp/dev.db ./investhub
```

5 秒一轮行情，配临时数据库，不污染正式数据。

### 代码检查

```bash
gofmt -w cmd internal
go vet ./...
go build ./...
```

### 前端开发约定

- **删除确认**：使用 `await app.confirm({ title, message, danger })` 替代原生 `confirm()`，统一样式
- **API 调用**：`Promise.all` 中所有调用都必须加 `.catch()`，防止单点失败全盘 reject
- **模板访问**：computed 可能为 null 时使用可选链 `?.`
- **数字格式化**：检查 `Number.isNaN()`，防止模板渲染出 "NaN" 字符串
- **SSE 连接**：已在 store.js 实现指数退避自动重连，页面无需额外处理
- **弹窗**：`ModalDialog` 组件已内置背景滚动锁定，直接用即可

---

---

## 变更日志

### v1.2.0（2026-08-06）

**安全性**
- Settings 更新增加 key 白名单校验，防止覆盖内部状态
- JSON 备份导入增加列名正则校验，防止 SQL 注入
- SSE 长连接不再触发 session 滑动续期
- Webhook 调用增加 10s 超时（原 `http.DefaultClient` 无超时）
- AI 分析支持 `context.Context` 取消（用户关闭浏览器时不再阻塞）

**用户体验**
- 页面切换增加淡入淡出过渡动画（180ms）
- 仪表盘 StatCard 加载时显示脉冲骨架屏，消除布局跳动
- ECharts 图表无数据时显示引导提示，而非空白 canvas
- 全局统一确认弹窗替换浏览器原生 `confirm()`，统一品牌视觉
- 按钮、导航、标签增加 0.15s 过渡效果
- SettingsView 汇率表区分「加载中」与「空数据」状态

**健壮性**
- POLL_INTERVAL 增加 600s 上限
- `rand.Read` salt 失败时记录日志（不再静默忽略）
- `os.Getwd` 错误不再被忽略
- `scheduler.Stop()` 支持安全多次调用
- SSE 写入错误检测客户端断开（避免持续写无效数据）
- AI 响应体限制 1MB，防止内存暴涨
- CSV 文件上传限制 10MB
- 调度 goroutine 增加外层 panic recovery

**前端修复**
- AssetDetailView：`Promise.all` 全部调用加 `.catch()`，模板改用可选链
- DashboardView：全部 API 失败时显示错误提示与重试链接
- Store：SSE 断线实现指数退避自动重连（1s→…→30s）
- Store：SSE 事件数组限制 200 条上限防止内存增长
- WatchlistView：目标价输入 NaN 校验
- AlertsView：阈值字段 NaN 校验
- format.js：`dirClass` 改用 `Math.abs(v) < 1e-10` 处理浮点精度

### v1.1.0（2026-08-06 初版）
初始发布：仪表盘、持仓、自选、交易流水、AI 分析、提醒、多币种汇率、
基准对比、现金账户、JSON/CSV 备份、16 张数据表、单二进制部署。

---

## 常见问题

**Q：必须联网吗？**
不必须。断网时行情自动降级到内置模拟器（随机游走），所有功能照常可用。想固定用模拟器，把「设置」里的数据源模式改为 `sim`。每条行情的 `status` 字段（`ok`/`sim`/`stale`）会如实反映数据来源。

**Q：没有 DeepSeek API Key 能用 AI 分析吗？**
能。会自动使用本地启发式引擎，基于 RSI、MACD、均线、涨跌幅等指标输出规则化结论，返回结构与调用大模型时完全一致。

**Q：怎么删掉演示数据？**
在「持仓」页逐个删除标的，或直接 `rm -rf data/` 从零开始（会同时清掉你自己录入的数据，请先备份）。

**Q：curl 请求 localhost 返回 502？**
检查是否设置了 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量 —— curl 会把 localhost 也走代理。加 `--noproxy '*'` 或 `NO_PROXY='*'` 即可。**Go 服务本身不会返回 502**，浏览器访问不受影响。

**Q：`go build` 后首页空白？**
说明 `internal/web/dist` 是空的。执行 `cd frontend && npm install && npm run build` 生成产物后重新 `go build`。

**Q：`frontend/node_modules` 不见了，是不是缺文件？**
不是，是**刻意不保存**的。前端产物已构建进 `internal/web/dist` 并嵌入二进制，日常开发和 `go build` 都用不到它。只有要改 Vue 源码时才 `cd frontend && npm install`，`package-lock.json` 会精确还原版本。这让仓库从 97 MB 降到 200 KB。

**Q：数据库能直接用 sqlite3 打开吗？**
可以，就是标准 SQLite 文件。但 `settings` 表里的敏感字段是密文，需要 `master.key` 才能解。

**Q：支持多用户吗？**
不支持，这是**单人自用**工具。访问口令的作用是防止同一台机器 / 局域网上的他人误入，不是多租户体系。

**Q：Webhook 提醒支持哪些服务？**
任何接受 `POST` 且能解析 JSON 的服务均可，例如企业微信 / 钉钉 / 飞书的自定义机器人。触发时 InvestHub 向 `webhook_url` 发送 `{"source":"InvestHub","message":"...","time":...}`；HTTP 状态码 ≥ 400 视为发送失败并记录。可在「设置」页点「发送测试消息」先验证连通性。

---

## 许可

个人自用项目，代码可自由修改使用。行情数据来自 Binance 与新浪财经的公开接口，请遵守各自的使用条款。
