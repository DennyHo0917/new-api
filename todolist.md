# API Route 自建后端开发计划与任务清单 (TodoList)

## 一、 项目背景与目标

### 1.1 业务现状与核心痛点
* **前端资产**：拥有现成的前端分销站点 `api-route-deploy`（基于 Vite + React 18 开发），已部署在 Vercel 并绑定正式域名 `www.api-route.com`。
* **上游受制**：此前前端的 API 请求全部反向代理到第三方平台 SubRouter（`https://apiroute.subrouter.ai`）。平台不开放后端源码与数据库权限，客户资源、交易流水和 API Key 均受制于上游。
* **垫资痛点（核心商业防线）**：
  * 老用户之前充值的款项已被 SubRouter 收取，存量余额在旧站。
  * **绝不能在新站自行垫资垫付旧余额**（否则将由站长个人资金承担大模型调用成本）。
  * 必须建立**双轨透明网关**：老用户的旧 API Key 流量透明透传给 SubRouter 扣除旧余额（站长成本为 0）；待旧余额耗尽或用户发起新充值时，资金进入站长自有收款通道，请求无缝切入新站独立后端。

### 1.2 核心目标
1. **建立完全自主可控的 New API 后端**，支持 SQLite、MySQL、PostgreSQL 三大数据库。
2. **实现接口契约完全兼容**：后端提供全套 `/api/dist/*` 路由规范，使现有的 `api-route-deploy` 前端无需重构业务代码即可无缝切换。
3. **基于 TxHash 验证的加密货币充值能力**：支持 **Arbitrum One (USDT/USDC)** 与 **TRON (TRC20 USDT)** 等链，用户转账后提供交易哈希（TxHash），后端链上查询确认，**严格以钱包实际入账的代币金额为准**自动换算 Quota 上账。
4. **实现双轨智能路由网关**：根据 API Key 自动识别并分流（本地新 Key 走本地渠道，未迁移旧 Key 透传 SubRouter 扣旧额度），实现“零垫资、无感过渡”。

---

## 二、 详细开发任务清单 (TodoList)

> **执行规范**：依据 `AGENTS.md` 约束，每完成一项任务，必须立即将对应条目的 `[ ]` 勾选为 `[x]` 并注明完成记录。

### 模块一：数据库模型与系统配置扩展 (Database & Config)
- [x] **1.1 加密货币充值订单数据模型 (`CryptoTransaction`)** *(已完成并通过自动化测试)*
  - [x] 设计模型字段：
    - `id`: 主键
    - `trade_no`: 内部唯一订单号（如 `CRYPTO-20260916-XXXX`）
    - `user_id`: 充值用户 ID
    - `chain`: 链标识（`arb` / `tron` 等）
    - `token`: 代币标识（`USDT` / `USDC`）
    - `wallet_address`: 平台收款钱包地址
    - `expected_amount`: 用户预估充值金额
    - `actual_amount`: **链上实际入账金额（以此为准结算 Quota）**
    - `quota_amount`: 实际到账折算的配额
    - `tx_hash`: **用户提交的链上交易哈希（全局唯一，防重放核心）**
    - `from_address`: 付款方钱包地址（链上解析存证）
    - `status`: 订单状态（`pending`: 待提交哈希/等待确认, `processing`: 链上确认中, `success`: 充值成功, `failed`: 校验失败, `expired`: 超时关闭）
    - `fail_reason`: 失败原因（如非目标地址、合约不符、交易失败等）
    - `expired_at`: 订单过期截止时间
    - `created_at`, `updated_at`
  - [x] 索引与唯一约束设计：
    - `trade_no` 唯一索引
    - `tx_hash` 唯一约束（防止同一笔转账哈希被重复提交/多次上账）
    - `user_id + status` 用户订单索引
  - [x] 适配 GORM 自动迁移，确保 SQLite、MySQL (>=5.7.8)、PostgreSQL (>=9.6) 三库完全兼容 (已注册至 `model/main.go` AutoMigrate 并通过单元测试验证)
- [x] **1.2 套餐与订阅数据模型设计** *(已完成并注册迁移)*
  - [x] 设计 `Package` 套餐模型（名称、原价、现价、周期、包含额度、状态等，见 `model/package.go`）
  - [x] 设计 `UserPackageSubscription` 订阅记录模型（用户ID、套餐ID、起止时间、状态）
  - [x] 确保多数据库迁移幂等与兼容性 (已注册至 `model/main.go` AutoMigrate)
- [x] **1.3 系统运营配置项扩展 (`setting/operation_setting`)** *(已完成)*
  - [x] 新增支持的加密链与钱包地址配置：
    - `ArbWallet`: Arbitrum One 收款地址（EVM 0x...）
    - `TronWallet`: TRON (TRC20) 收款地址（波场 T...）
    - 各链官方 USDT / USDC 合约地址白名单
  - [x] 新增链上 RPC 节点 / API 配置（如 Arbitrum 公开 RPC / Infura / Alchemy，TronGrid API Key）
  - [x] 新增最小充值金额与订单有效期配置（默认 30 分钟）
  - [x] 新增站点公告、汇率（USD/CNY）、品牌展示配置存储与动态读取 (见 `setting/operation_setting/crypto_setting.go`)

---

### 模块二：基于 TxHash 链上验证与实际金额入账引擎 (Crypto Engine via TxHash)
- [x] **2.1 订单创建接口 (`POST /api/dist/topup/crypto/pay`)** *(已在 `controller/crypto.go` 完成；已补充 NaN/Inf/正数/系统上限校验，Arbitrum 与 TRON 收款地址已配置为默认公开地址，仍可由环境变量覆盖)*
  - [x] 鉴权校验（解析登录态，获取当前用户 ID）
  - [x] 入参校验：充值金额 `amount`、选择的链 `chain`（`arb` / `tron`）、代币 `token`（`USDT` / `USDC`）
  - [x] 生成订单入库（初始状态 `pending`），返回平台对应的收款钱包地址、订单号 `trade_no` 及有效时间
  - [x] 加密货币最低充值默认值调整为 $5
- [x] **2.2 用户提交交易哈希接口 (`POST /api/dist/topup/crypto/submit`)** *(已在 `controller/crypto.go` 完成)*
  - [x] 入参：`trade_no`（订单号）、`tx_hash`（用户转账后的交易哈希）
  - [x] 防重校验：检查数据库中该 `tx_hash` 是否已被使用，防止重放攻击
  - [x] 绑定 `tx_hash` 到订单，将状态置为 `processing`，触发链上实时核验
- [x] **2.3 链上多链交易核验与实际金额读取服务** *(已在 `service/crypto_service.go` 完成并通过自动化测试)*
  - [x] **TRON (TRC20 USDT) 链上验证器**：
    - [x] 调用 TronGrid / 波场节点接口查询该 `tx_hash` 交易详情及合约调用事件
    - [x] 严格校验交易状态（`contractRet == "SUCCESS"` 且已完成打包确认）
    - [x] 严格比对目标合约地址为官方 USDT 合约（`TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t`）
    - [x] 严格比对接收地址为平台的 `TronWallet`
    - [x] **提取交易实际转移的代币数量（精度 6 位），得到 `actual_amount`**
  - [x] **Arbitrum One (USDT/USDC) 链上验证器**：
    - [x] 调用 Arbitrum RPC（`eth_getTransactionReceipt`）查询交易回执
    - [x] 校验 `status == "0x1"`（执行成功）
    - [x] 解析 Transfer 日志事件（`Topic[0] == Transfer`）：
      - 校验合约地址为官方 Arb USDT (`0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9`) 或 USDC (`0xaf88d065e77c8cC2239327C5EDb3A432268e5831`)
      - 校验接收方地址为平台的 `ArbWallet`
      - **从 Data 提取实际转入代币数额（精度 6 位），得到 `actual_amount`**
  - [x] **交易超时与失败容错**：若链上暂未确认，保持 `processing` 由定时任务继续重试
- [x] **2.4 实际入账结算与 Quota 上账事务** *(已在 `model/crypto_transaction.go` + `service/crypto_service.go` 完成)*
  - [x] 开启数据库事务：
    - [x] 校验 `actual_amount > 0`
    - [x] 根据 `actual_amount` 严格计算用户应得 Quota（1 USDT = 1 USD Quota = 500,000 配额，使用 `common.WalletQuotaFromDecimalStrict` 杜绝溢出）
    - [x] 原子递增当前用户的 `quota`
    - [x] 更新订单状态为 `success`，写入 `actual_amount` 与 `quota_amount`
    - [x] 写入充值历史记录（`top_ups`）
  - [x] 事务提交，实时生效
- [x] **2.5 订单状态轮询接口 (`GET /api/dist/topup/crypto/status`)** *(已在 `controller/crypto.go` 完成；已补充当前用户订单归属校验，阻止跨用户读取订单状态、TxHash 与到账信息)*
  - [x] 接收 `trade_no`，返回当前订单的最新状态（`pending` / `processing` / `success` / `failed` / `expired`）以及实际到账数额 `actual_amount`
  - [x] 适配前端轮询，检测到 `success` 立即弹窗提示并刷新前端余额
- [x] **2.6 前端交互适配 (`Topup.jsx` 扩展提交 TxHash)** *(已在 `api-route-deploy-new` 完成并构建成功)*
  - [x] 在前端加密货币支付弹窗中，增加“已完成转账？粘贴交易哈希（TxHash）”输入框与“提交哈希立即验证”按钮
  - [x] 支持用户在转账后粘贴哈希立即提交核验，极速到账
  - [x] 支持 Arbitrum One、TRON 网络选择与中英文国际化文本补齐

---

### 模块三：SubRouter 接口协议兼容层 (`/api/dist/*` 路由群开发)
- [x] **3.1 兼容路由组骨架注册 (`router/dist-router.go`)** *(已完成并挂载至主路由)*
  - [x] 挂载 `/api/dist` 顶级路由组，注入标准鉴权中间件与请求头解析（支持 `New-Api-User` 标头与 Session Cookie 互通）
- [x] **3.2 站点公开信息与定价接口** *(已在 `controller/dist.go` 完成)*
  - [x] `GET /api/dist/site/info`：返回站点名称、模板主题、启用支付方式列表（包含 `crypto`）、货币符号与汇率
  - [x] `GET /api/dist/site/models`：动态输出当前系统启用的可用模型列表
  - [x] `GET /api/dist/site/pricing`：返回各个模型的定价详情与分组倍率
  - [x] `GET /api/dist/site/packages`：返回在售套餐包列表
  - [x] `GET /api/dist/topup/info`：返回支持的支付方式、加密货币钱包配置（Arb/Tron 钱包地址）、最小充值额度等
  - [x] `POST /api/dist/topup/amount`：根据汇率动态计算支付金额与到账额度
- [x] **3.3 用户体系接口兼容 (`/api/dist/user/*`)** *(已在 `controller/dist.go` 完成)*
  - [x] `POST /api/dist/user/register`：用户注册接口适配
  - [x] `POST /api/dist/user/login`：用户登录接口适配（包含双轨账号探测）
  - [x] `POST /api/dist/user/logout`：退出登录
  - [x] `GET /api/dist/user/self`：获取当前登录用户信息与 Quota 余额
  - [x] `PUT /api/dist/user/password`：修改密码接口适配
  - [x] `GET /api/dist/user/usage`：用户调用量折线图统计
  - [x] `GET /api/dist/user/logs` & `/api/dist/user/logs/stat`：模型调用明细日志与聚合统计
- [x] **3.4 API Key 令牌管理接口 (`/api/dist/token/*`)** *(已在 `controller/dist.go` 完成)*
  - [x] `GET /api/dist/token/list`：分页查询用户的 API Keys（直接返回数组兼容前端）
  - [x] `POST /api/dist/token/create`：创建新的 API Key（生成 `sk-...` 令牌）
  - [x] `PUT /api/dist/token/:id`：修改 Key 名称、过期时间、配额限制
  - [x] `DELETE /api/dist/token/:id`：删除或作废 Key
  - [x] `GET /api/dist/token/:id/models`：查询该 Key 允许访问的模型列表
- [x] **3.5 传统支付与兑换码接口打通** *(已在 `controller/dist.go` 完成)*
  - [x] `POST /api/dist/topup/redeem`：卡密/兑换码兑换额度接口
  - [x] `POST /api/dist/topup/pay`：易支付（微信/支付宝）下单对接
  - [x] `POST /api/dist/topup/stripe/pay`：Stripe 信用卡支付对接
  - [x] 分站兼容接口按 Stripe API/Webhook/Price 与合规配置动态启用 Stripe，最低充值默认值调整为 $5
  - [x] `GET /api/dist/topup/history`：充值记录明细查询
- [x] **3.6 套餐购买与订阅结算 (`/api/dist/package/*`)** *(已在 `controller/dist.go` 完成)*
  - [x] `POST /api/dist/package/subscribe`：套餐扣减余额购买与激活逻辑
  - [x] `GET /api/dist/package/subscriptions`：查询用户当前生效中的套餐包

---

### 模块四：双轨智能网关分流（零垫资旧站流量接管）
- [x] **4.1 网关 Key 探测与分流中间件** *(已在 `middleware/dist_gateway.go` + `service/dist_proxy.go` 完成并通过测试；2026-09-16 修复为旧 Key 只有在 SubRouter 明确额度耗尽后才切换本地，并持久化 exhausted 状态)*
  - [x] 在 `/v1/*` 核心中转路由前置检查 `Authorization: Bearer <key>`、`x-api-key`、`mj-api-secret` 等
  - [x] 校验逻辑：
    - 若 `<key>` 存在于本地且对应账户可用（已充值有配额） -> 执行本地 New API 渠道中转扣费
    - 若 `<key>` 在本地未查到（判定为旧站老用户 Key） -> 执行反向代理透传至 `https://apiroute.subrouter.ai/v1/*`
    - 若 `<key>` 属于迁移导入的老 Key 且新站额度为 0 -> 优先透传 SubRouter 扣除旧站残留余额，零垫资
  - [x] 透传时保留原始 HTTP Header、流式（SSE）长连接与超时配置 (`FlushInterval = -1`, `ResponseHeaderTimeout = 300s`)
- [x] **4.2 旧额度耗尽与充值引导** *(已在 `service/dist_proxy.go` 完成并通过测试；2026-09-16 增加上游额度耗尽标记回传，避免本地余额提前垫付)*
  - [x] 捕获 SubRouter 返回的 `429 Insufficient Quota` 及额度不足错误
  - [x] 格式化向前端输出友好提示，引导用户前往 `https://www.api-route.com/topup` 充值
- [x] **4.3 用户首次登录自迁移（密码与历史 Key 抓取）** *(已在 `service/dist_migration.go` + `controller/dist.go` 完成并通过测试)*
  - [x] 用户登录时若本地未录入密码，先代理向上游 SubRouter 验证凭据
  - [x] 验证成功自动将密码加密存入本地库（严格设置初始 Quota = 0，恪守零垫资原则）
  - [x] 利用已认证 Session 自动抓取导入该用户的历史 API Key 列表，并打标 `Group = "subrouter"`
- [x] **4.4 SubRouter 历史存量客户全量同步工具与数据库写入** *(已完成)*
  - [x] 逆向/解析 SubRouter 分销商管理鉴权机制（Session Cookie + `New-Api-User: 4870` 请求头）
  - [x] 开发 `cmd/sync_customers` CLI 批量同步与入库工具，支持在线拉取与本地快照载入
  - [x] 成功抓取全部 520 位存量客户数据，生成完整数据快照 `subrouter_customers_backup.json`
  - [x] 全量 520 位客户写入本地数据库 `users` 表，实现 0 失败率：
    - [x] 针对同名/冲突用户名自动按客户 ID 进行唯一化重命名（如 `poc_20093`）
    - [x] 批量生成全局唯一 `aff_code`，规避数据库唯一索引约束冲突
    - [x] 严格落实“零垫资”防线（新站 Quota 一律设为 0，历史配额与使用量存证于 `Setting` 与 `Remark`）
    - [x] 单元测试 `TestSyncCustomersFromRecords` 100% 覆盖并验证写入与更新幂等性

---

### 模块五：全链路联调验证与生产割接 (Verification & Cutover)
- [ ] **5.1 本地联调测试**
  - [x] 已完成后端兼容路由编译检查、核心迁移/网关/链上验证定向测试、`api-route-deploy-new` 生产构建，以及本地代理、站点信息、注册、登录、用户信息和 API Key 创建联调。
  - [x] 已配置临时 SQLite 渠道并使用运行时密钥完成真实 `/v1/chat/completions` 非流式与流式测试；前端代理转发、模型响应和本地额度扣减均正常。
  - [x] 未修改只读生产前端；`api-route-deploy-new/vite.config.js` 的本地代理已指向 `http://127.0.0.1:3000` 验证。
  - [ ] 验证页面渲染：首页、模型广场、定价页面是否正常（HTTP 接口已验证，浏览器视觉检查待补）。
  - [x] 验证用户流程：注册、登录、获取用户信息、创建 API Key。
  - [ ] 验证充值流程：发起 USDT/USDC（Arb/Tron）充值、提交转账哈希、链上核验实际金额、自动充值到账
  - [x] 验证 API 调用：使用本地新 Key 请求 `/v1/chat/completions` 非流式和流式打字机测试。
  - [x] 验证零垫资路由边界：未知旧 Key 转发 SubRouter，收到上游 401 时本地用户额度未变化；网关定向回归测试通过。
  - [x] 提交前核心定向测试通过；全量 `controller` 测试仍有既有 Windows/SQLite 临时库清理阶段的 `database is locked` / 文件占用失败，未发现本次改动相关失败。
- [ ] **5.2 VPS 生产环境构建与部署**
  - [x] 提交代码并由 GitHub Actions 自动构建部署至 VPS（提交 `686dce6df`，Deploy API Route #6 成功，耗时 3m56s）
  - [ ] 通过部署密钥注入 Stripe、ZPay、GitHub、Google、X 配置，后端不得持久化或输出明文密钥
    - [x] 完成环境变量覆盖、Compose 透传和 GitHub Actions 安全传递代码；敏感值不进入 OptionMap、数据库或仓库
    - [ ] 写入仓库 Actions Secrets 并验证容器实际读取（当前 `gh` 未登录且本机访问 GitHub 设备登录 TLS 超时）
  - [ ] 接通并回归 `/api/dist/oauth/{google,github,x}` 登录兼容接口与 Stripe/ZPay 支付回调
    - [x] 完成分销站 OAuth start/callback、PKCE S256、10 分钟服务端 state、支付方式发现及定向回归测试
    - [ ] 在部署环境完成三方真实回调与支付沙盒/小额实测；本机对 Stripe/ZPay 的 TLS 握手失败，未将网络失败误判为凭据失败
  - [ ] 更新 VPS 的 Nginx 配置，确保 `/api/dist/*` 正确转发到 Go 后端容器
  - [ ] 配置链上验证所需的 RPC 节点、API Key 与平台收款钱包地址
- [ ] **5.3 前端生产切流割接**
  - [ ] 在不改变 `www.api-route.com` 与 `global.api-route.com` 的前提下，为新后端准备独立源站域名与 SSL（不得提前切流）
  - [ ] 修改可编辑前端 `api-route-deploy-new/vercel.json` 的候选转发规则并验证回滚；生产前端未经用户明确指令不得修改
  - [ ] 将 `apiroute.subrouter.ai` 配置为新后端的普通上游渠道，验证新后端用户、Key、计费与零垫资边界
  - [ ] 触发 Vercel 构建上线，观察生产流量与日志，完成整体业务平滑接管
