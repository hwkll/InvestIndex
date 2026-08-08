# InvestHub — 项目长期记忆

## 是什么
个人综合投资管理平台（PRD v1.1 落地）。**技术栈：Go + Vue3 + ECharts，单二进制自托管**，本地/隐私优先、零配置。管理层级：crypto / fund / gold / stock 四类资产，含行情、收益、技术指标、AI 分析(DeepSeek)、提醒、现金账户、JSON/CSV 备份。

## 架构要点
- 后端 `internal/` 分层：store / cryptox / settings / indicators / quotes / core / ai / alerts / api / web；入口 `cmd/investhub`。
- 数据库 `modernc.org/sqlite`（cgo-free 纯 Go）→ 单静态二进制。路由 `go-chi/chi/v5`。加密 `golang.org/x/crypto`（scrypt 口令哈希 + AES-256-GCM 敏感配置）。
- 前端 `frontend/`（Vue3.5 + Vite5 + ECharts5）构建产物 `frontend/dist` 经 `internal/web/web.go` 的 `//go:embed all:dist` 嵌入 → 真正单二进制。`INVESTHUB_WEB_DIR` 可指向磁盘目录免重建。
- API 统一信封 `{code,data,msg}`；路径前缀 `/api/v1`；端口 7788；SSE `/api/v1/events` 事件类型 `quote`/`alert`/`ai_done`。
- 数据源：模拟器默认（随机游走），联网自动启用 CoinGecko/新浪并优雅降级。`data_source_mode=auto|real|sim`。
- 认证：无 PIN 则开放；设 PIN(≥6) 后启用 HttpOnly 会话 Cookie（30 天滑动），5 次失败锁 5 分钟。SSE 也受 authGuard 保护。

## 构建 / 运行
```bash
cd frontend && npm install && npm run build
cd .. && go build -o investhub ./cmd/investhub && ./investhub
```
环境变量：PORT(7788) / HOST / POLL_INTERVAL(30) / INVESTHUB_DB(data/investhub.db) / INVESTHUB_WEB_DIR / INVESTHUB_SECRET(否则 data/master.key)。

## ⚠️ 两个必记的环境坑
1. **HTTP_PROXY 导致 curl 到 localhost 返回 502**：本机设了 `HTTP_PROXY/HTTPS_PROXY=http://127.0.0.1:50377`。本地验证服务必须加 `--noproxy '*'`（`NP=(--noproxy '*')` 在 zsh 里用数组，否则 `*` 被 glob 破坏）。Go 不返回 502，故 502=代理问题。
2. **vite build 需关闭沙箱**：受控沙箱里 `npm run build` 会被 SIGKILL(exit 137) 杀掉整个进程组；用 `dangerouslyDisableSandbox:true` 运行才能成功。普通 `go build` / 运行服务 / curl 不受影响。

## 关键领域事实（写文档/改代码前先核对，勿凭印象）
- **资产分类只有 4 类**：`crypto` 加密货币 / `fund` 基金 / `gold` 黄金 / `stock` 股票。**没有 etf 分类** —— ETF 是 `subType` 字段（如 510300、518880 的 category 分别是 fund / gold，subType="etf"）。
- **交易方向只有 `buy` / `sell`**，DB 层有 `CHECK(direction IN ('buy','sell'))` 硬约束。**不支持分红/拆股**。
- **HOST 默认为空 → 监听 `:PORT`，即所有网卡**（局域网可访问），不是 localhost。要限本机须显式 `HOST=127.0.0.1`。这是安全相关事实，README 已按此写。
- `data/investhub.db` 与 Go 版 schema 完全兼容（14 张表），Node 时代的 DB 可被 Go 二进制直接接管，无需迁移。
- **`/api/v1/cash-accounts` 返回的是对象 `{items,totalBalance,currency}` 不是数组**。测试脚本里 `len(d)` 会得到 3（key 数）而非账户数，必须取 `d["items"]`。曾两次误判为「账户数量 bug」，实为脚本错误。
- `SeedDemo()` 固定播种：5 标的（BTC/ETH/sh.600519/510300/518880）+ 5 笔买入 + **2 个现金账户**（招商银行卡 80000 + 华泰证券账户 30000 = 110000 CNY）。启动时另生成 90 天历史快照（kline_cache 900 / position_snapshots 450 / cash_snapshots 180 行）。

## 项目形态（2026-08-06 三轮清理后 · 最终）
根目录仅剩：`cmd/ frontend/ internal/ go.mod go.sum README.md .gitignore investhub`（**无 data/**）。
**总计 45 个源文件、18M（其中二进制占 17M）**：14 个 .go + 25 个前端源码 + 配置。
- `legacy-node/` 已彻底移除（备份 `legacy-node-*.tar.gz`）。全项目无任何 legacy 引用。
- **`frontend/node_modules` 已删除（97M→200K）**，备份 `frontend-src-*.tar.gz`。**这不是缺文件**：dist 已嵌入二进制，`go build` 与运行期都不依赖它；只有改 Vue 源码时才 `cd frontend && npm install`（package-lock 锁版本）。
- **`data/` 已删除**（备份 `data-legacy-*.tar.gz`）。**这也不是缺文件**：首次启动自动创建 db + master.key 并播种演示数据，已实测验证。
- `internal/web/dist/.gitkeep` 是刻意保留的占位文件 —— 没有它，未构建前端时 `//go:embed all:dist` 会直接编译失败。**勿删**。
- 三份备份都在 `~/.workbuddy/backups/InvestIndex/`。

## ⚠️ 清理时的判断准则（用户要求"清理"时照此执行）
- **`frontend/src` 绝不能删**：是当前最新版 Vue3 源码（非旧版）。删掉后 UI 永久冻结在已编译的 dist，无法再修改。
- **`data/` 要先查再决定，不要凭「像是用户数据」就拒删**。判据（用 sqlite3 查）：
  - `select count(*) from settings` —— 0 行说明**没有任何用户配置**，master.key 加密的内容为空，删除零损失。
  - `transactions.created_at` 若全部集中在同一秒 → 是 `SeedDemo()` 批量插入的演示数据，非用户录入。
  - `meta.onboarded='0'` → 用户从未在 UI 完成引导。
  - 三者都命中即可安全清理（2026-08-06 实测正是此情况，曾误判为「真实持仓数据」而拒删）。
- 删除前一律先 tar 备份到 `~/.workbuddy/backups/InvestIndex/`，再 `mv` 到 `~/.Trash`。
- macOS 上 `osascript` 移废纸篓会被 TCC 拒绝，`ls ~/.Trash` 也会 "Operation not permitted"；改用 `mv` 到 `~/.Trash` 或 tar 备份后 `rm`。
- 调试残留：`/tmp/ih_*`、`/tmp/vite_build.log` 可直接 rm。

## 验证状态（2026-08-06）
13/13 接口校验 PASS + SSE `event: quote` 广播确认；ai/analyses 持久化、CSV 导出、交易联动、positions、pnl/trend 均正常。
**最终回归（data/ 与 node_modules 全清后，从零状态）**：`go build`(17M) + `go vet` 全绿；启动自动重建 data/ 与演示数据（5 标的 / 5 交易 / 2 现金账户 110000 CNY）；9 个端点全 200；前端 assets 200。README.md 550 行完整文档（含 API 全表、数据模型、安全模型、FAQ）。

## 代码审计记录（2026-08-06 全面审查）
经历一次完整的 Go + Vue3 全量代码审计，修复 20+ bug。以下是审计后确认的安全/代码规范原则：

### Go 端规范
- **SQL 列名来自用户输入 → 必须正则校验**。`backup.go` JSON 导入的列名需 `^[a-zA-Z_][a-zA-Z0-9_]*$`。
- **Settings 更新 → 必须 key 白名单**。`handlers.go` 的 `handleUpdateSettings` 遍历 `map[string]any` 无过滤是危险的。
- **读锁释放后、写锁获取前 → 要 double check**。`quotes.go` `SeedState` 的 TOCTOU 模式。
- **`close(channel)` → 用 `select { case <-ch: default: close(ch) }`** 防止 double-close panic（`scheduler.go` `Stop()`）。
- **所有 goroutine 都要 recover**。仅内层 `tick()` 有 recover 不够，外层 goroutine 也要。
- **`http.DefaultClient` → 有超时限制的专用 client**。`alerts.go` webhook 调用原用无超时 DefaultClient。
- **长时间操作（AI 分析）→ 接受 `context.Context` 支持取消**。`ai.Analyze` 签名已改。
- **`rand.Read` 错误不能 `_, _` 忽略**。salt 为全零会弱化 scrypt。

### 前端规范
- **`Promise.all` 中所有调用都要 `.catch()`**。任一项失败会全盘 reject。
- **模板访问 computed 属性 → 用可选链 `?.`**。数据加载失败时 computed 可能返回 null。
- **数字格式化 → 检查 `Number.isNaN()`**。后端可能返回 NaN。
- **`EventSource` → 必须实现重连**。浏览器内建重连依赖服务器 `retry` 字段，不可靠。
- **事件数组 → 必须限制长度**。`events.unshift()` 无限增长吞内存。
- **页面加载失败 → 必须有 `v-else` 错误状态**。不能只有 loading 和正常内容两个分支。
- **用户输入 `parseFloat` → 检查 `isNaN`**。
- **`dirClass` 之类判零函数 → 用 `Math.abs(v) < 1e-10`** 而非 `v === 0`（浮点精度、-0）。
- **CSV 文件上传 → 限制大小**。无限制的大文件会卡死浏览器。
- **弹窗打开 → 锁 background scroll** (`body.overflow='hidden'`)。
