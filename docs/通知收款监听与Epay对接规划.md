# 通知收款监听与 EPay 对接规划

> 目标：把安卓端“通知监听”能力、安全地接入当前彩虹易支付项目，形成“收款通知归集 / 订单对账 / 商户查询与回调”的系统。
>
> 重要边界：通知监听只能作为到账线索与内部对账依据；如果对外提供支付收单、资金清算、特约商户接入等能力，需要走微信支付 / 支付宝官方商户体系、服务商体系或具备相应合规资质。不要把个人号通知监听包装成规避官方支付通道的第三方支付业务。

---

## 1. 当前项目判断

### 1.1 本地 EPay 项目

当前目录已有彩虹易支付项目：

- `/mnt/d/desktop/wxzf/Epay`
- 技术栈：PHP 7.4+、MySQL/MariaDB、无框架、多入口 PHP。
- 支付插件目录：`/mnt/d/desktop/wxzf/Epay/plugins`
- 订单创建入口：`/mnt/d/desktop/wxzf/Epay/includes/lib/api/Pay.php`
- 支付通道选择：`/mnt/d/desktop/wxzf/Epay/includes/lib/Channel.php`
- 插件加载：`/mnt/d/desktop/wxzf/Epay/includes/lib/Plugin.php`
- 支付成功处理：`/mnt/d/desktop/wxzf/Epay/includes/functions.php` 里的 `processNotify()` / `processOrder()`，最终会更新订单、加商户余额并通知商户。

结论：**不要大改 EPay 核心**。正确做法是新增一个支付插件和少量独立接口，把通知对账系统接进现有订单生命周期。

### 1.2 安卓监听项目初步审计

我把 `c2s/AndroidNotificationDispatcher` 克隆到临时目录做了静态查看：

- 临时目录：`/tmp/wxzf_android_notification_audit/AndroidNotificationDispatcher`
- 当前提交：`94bb566 更新README`

初步发现：

1. `/tmp/wxzf_android_notification_audit/AndroidNotificationDispatcher/app/src/main/java/cn/imofei/notificationdispatcher/Message.java`
   - 默认上报地址写死为：`http://pay.qingsonge.com/addons/pay/api/notify1`
   - 会把通知内容 JSON 通过 OkHttp POST 出去。
   - Header 里带 `token`、`secret`、`device`。

2. `/tmp/wxzf_android_notification_audit/AndroidNotificationDispatcher/app/src/main/java/cn/imofei/notificationdispatcher/activity/MainActivity.java`
   - `API_URL` 同样写死为上述第三方 HTTP 地址。
   - 页面只让用户填 token、secret、device_id，没有明显的服务端 URL 输入。

3. `/tmp/wxzf_android_notification_audit/AndroidNotificationDispatcher/app/src/main/AndroidManifest.xml`
   - 权限主要是网络权限和通知监听服务权限。
   - `android:allowBackup="true"`，生产环境不建议保留。

4. `/tmp/wxzf_android_notification_audit/AndroidNotificationDispatcher/app/release/epay-release.apk`
   - APK 内 `classes.dex` 字符串也能看到 `http://pay.qingsonge.com/addons/pay/api/notify1`。

结论：

- 这不能直接证明“有后门”，但已经足够说明：**不能直接安装它仓库里的 release APK**。
- 原项目更像一个旧的演示版 / 商业引流版，默认把通知发到作者服务端。
- 推荐策略：**只参考实现思路，不直接使用原 APK；自己 fork 后删掉硬编码地址，重写鉴权、加密、重试、日志脱敏和签名校验，再自行编译。**

---

## 2. 推荐总体架构

```text
外部商户 / 自有业务系统
        │
        │  下单 / 查询 / 异步回调
        ▼
彩虹易支付 EPay
        │
        │  新增 notifyledger 插件
        ▼
收款管理端 / 通知对账中心
        │
        ├── 设备管理
        ├── 收款账号管理
        ├── 通知流水入库
        ├── 订单匹配与人工复核
        ├── 统计报表
        └── 安全回调给 EPay
        ▲
        │ HTTPS + HMAC + 重放保护
安卓通知监听端
        │
        ├── 微信 / 支付宝通知监听
        ├── 本地解析与脱敏
        ├── 本地队列与失败重试
        └── 设备心跳 / 权限状态上报
```

核心原则：

1. **安卓端只负责采集通知，不直接改 EPay 订单。**
2. **收款管理端负责可信判断：去重、匹配、复核、风控。**
3. **EPay 只负责商户下单、支付页、订单状态和商户回调。**
4. **支付成功必须走 EPay 原生 `processNotify()` 生命周期，避免绕过余额、分润、通知和风控逻辑。**

---

## 3. 系统拆分

### 3.1 安卓通知监听端

职责：

- 监听微信 / 支付宝通知。
- 过滤非收款通知。
- 解析基础字段：支付渠道、通知标题、通知内容、通知时间、设备时间。
- 本地生成事件 ID，防止重复上报。
- HTTPS 上报到收款管理端。
- 本地持久化队列，网络失败后重试。
- 上报设备心跳、权限状态、电量状态、App 版本。

必须重写 / 加固点：

- 删除硬编码第三方 URL。
- URL、device_id、device_secret 从你的后台绑定下发或扫码配置。
- 禁止 HTTP，只允许 HTTPS。
- 请求签名：`timestamp + nonce + body` 做 HMAC。
- 服务端校验时间窗、nonce、签名和设备状态。
- 日志脱敏，不打印完整通知 JSON、token、secret。
- `allowBackup=false`。
- 不使用仓库自带 release APK，必须自己编译签名。

### 3.2 收款管理端 / 通知对账中心

职责：

- 管理手机设备。
- 管理收款账号。
- 维护账号在线状态、最近心跳、最近通知时间。
- 接收通知流水。
- 标准化不同渠道通知格式。
- 根据订单池匹配到账。
- 对模糊匹配进入人工复核。
- 成功后回调 EPay 内部接口，由 EPay 标记订单成功。
- 提供统计：按账号、渠道、日期、金额、商户、订单状态统计。

建议独立于 EPay 做一个小服务，原因：

- EPay 是旧式 PHP 多入口项目，不适合承载设备长连接、队列、审计和复杂匹配。
- 独立服务更容易做消息队列、重试、权限、灰度发布。
- EPay 只保留支付网关职责，减少改坏核心支付逻辑的风险。

可选技术：

- 后端：PHP 独立模块 / Go / Node.js / Python FastAPI 均可。
- 数据库：优先共用 MySQL，但建议独立表前缀，例如 `nl_`。
- 队列：前期可用数据库队列；后期再加 Redis / RabbitMQ。

### 3.3 彩虹易支付 EPay 插件层

新增插件建议命名：

- 插件目录：`/mnt/d/desktop/wxzf/Epay/plugins/notifyledger`
- 插件主文件：`/mnt/d/desktop/wxzf/Epay/plugins/notifyledger/notifyledger_plugin.php`

插件职责：

1. 下单后向收款管理端申请一个“待收款会话”。
2. 返回支付页或二维码页给用户。
3. 用户付款后，等待收款管理端通过内部接口确认。
4. 确认后调用 EPay 原有 `processNotify($order, ...)`。

不建议做的事：

- 不要在安卓端直接调用 EPay 的商户回调。
- 不要在安卓端直接更新 `pre_order`。
- 不要绕过 `processNotify()`，否则容易漏掉商户余额、通知重试、黑名单、日限额、分润等逻辑。

---

## 4. 核心数据模型建议

### 4.1 设备表 `nl_device`

字段建议：

- `id`
- `device_no`
- `device_name`
- `secret_hash`
- `status`
- `app_version`
- `last_heartbeat_at`
- `last_ip`
- `battery_level`
- `notification_permission`
- `created_at`
- `updated_at`

### 4.2 收款账号表 `nl_account`

字段建议：

- `id`
- `channel`：`wechat` / `alipay`
- `account_alias`
- `account_identifier`
- `device_id`
- `status`
- `daily_limit_amount`
- `daily_received_amount`
- `last_notify_at`
- `remark`

### 4.3 通知流水表 `nl_notification_event`

字段建议：

- `id`
- `event_id`：安卓端生成，唯一。
- `device_id`
- `account_id`
- `channel`
- `raw_title`
- `raw_text`
- `parsed_amount`
- `parsed_payer`
- `notify_time`
- `received_at`
- `match_status`：`pending` / `matched` / `ambiguous` / `ignored`
- `matched_trade_no`
- `raw_payload_hash`

### 4.4 收款会话表 `nl_collect_session`

字段建议：

- `id`
- `epay_trade_no`
- `epay_out_trade_no`
- `uid`
- `channel`
- `account_id`
- `amount`
- `status`：`waiting` / `paid` / `expired` / `manual_review` / `closed`
- `expire_at`
- `paid_at`
- `notification_event_id`
- `match_score`
- `created_at`
- `updated_at`

### 4.5 审计表 `nl_audit_log`

记录所有关键动作：

- 设备绑定。
- 密钥轮换。
- 通知入库。
- 匹配成功。
- 人工改状态。
- 内部回调 EPay。
- 商户通知重试。

---

## 5. 订单匹配规则

推荐匹配条件：

1. 渠道一致：微信订单只匹配微信通知。
2. 收款账号一致：订单分配给哪个账号，就只匹配该账号通知。
3. 金额一致：通知金额与订单金额一致。
4. 时间窗口合理：通知时间在订单创建后、过期前。
5. 未重复使用：一条通知只能匹配一个订单。
6. 订单状态必须是待支付。

匹配结果分三类：

- **确定匹配**：唯一候选，自动置为成功。
- **模糊匹配**：同账号、同金额、同窗口出现多个待支付订单，进入人工复核。
- **无匹配**：作为孤儿通知保留，用于补单或人工确认。

注意：通知是外部 App 文案，不是银行级交易凭证。生产环境里，通知只能作为对账证据之一；关键交易最好结合官方账单、交易查询或人工复核。

---

## 6. 与 EPay 的接入方式

### 6.1 下单链路

```text
商户调用 EPay 下单接口
        ↓
EPay 创建 pre_order，状态 status=0
        ↓
Channel::submit() 选择 notifyledger 通道
        ↓
notifyledger_plugin::submit()
        ↓
向收款管理端创建 nl_collect_session
        ↓
返回支付页 / 二维码页
```

### 6.2 到账链路

```text
安卓端监听到微信 / 支付宝到账通知
        ↓
POST /device/notifications
        ↓
收款管理端验签、入库、解析
        ↓
匹配 nl_collect_session
        ↓
内部回调 EPay：确认 trade_no 已到账
        ↓
EPay 查询 pre_order 并调用 processNotify()
        ↓
EPay 更新订单、加余额、通知商户
```

### 6.3 查询链路

外部商户仍然走 EPay 原本能力：

- 创建订单。
- 查询订单状态。
- 接收异步通知。
- 跳转同步返回页。

这样外部商户不需要知道后面是哪个支付插件。

---

## 7. 接口规划

### 7.1 安卓端 → 收款管理端

#### 设备心跳

`POST /api/device/heartbeat`

包含：

- `device_no`
- `app_version`
- `battery_level`
- `network_type`
- `notification_permission`
- `timestamp`
- `nonce`
- `sign`

#### 通知上报

`POST /api/device/notifications`

包含：

- `event_id`
- `device_no`
- `package_name`
- `channel`
- `title`
- `text`
- `notify_time`
- `local_time`
- `timestamp`
- `nonce`
- `sign`

### 7.2 EPay → 收款管理端

#### 创建收款会话

`POST /internal/collect-sessions`

包含：

- `trade_no`
- `out_trade_no`
- `uid`
- `channel`
- `amount`
- `expire_at`
- `return_url`
- `notify_url`

返回：

- `session_id`
- `account_id`
- `pay_page_url`
- `qr_code_url` 或二维码内容。

### 7.3 收款管理端 → EPay

#### 内部确认到账

`POST /internal/notifyledger/paid`

包含：

- `trade_no`
- `event_id`
- `channel`
- `amount`
- `paid_at`
- `buyer_hint`
- `timestamp`
- `nonce`
- `sign`

EPay 侧必须：

- 校验内部签名。
- 查订单是否存在。
- 查订单是否待支付。
- 查金额是否一致。
- 查事件是否已处理。
- 调用 `processNotify()`。

---

## 8. 安全要求

### 8.1 安卓端安全

- 不用第三方 APK。
- 自己签名，自建发布流程。
- `allowBackup=false`。
- 禁止明文 HTTP。
- 敏感配置加密存储或至少使用 Android Keystore。
- 日志脱敏。
- 本地队列加上最大容量和过期清理。

### 8.2 服务端安全

- 所有内部接口都要 HMAC 签名。
- `timestamp` 建议允许 5 分钟误差。
- `nonce` 落库防重放。
- 设备密钥支持轮换。
- 内部接口不对公网开放；如果必须公网访问，至少加 IP 白名单和 WAF。
- 商户回调 URL 保持 SSRF 防护。
- 所有人工改状态必须写审计日志。

### 8.3 订单安全

- 幂等：同一个 `event_id` 只能成功处理一次。
- 幂等：同一个 `trade_no` 只能从待支付变为成功一次。
- 金额必须精确到分，不使用浮点直接比较。
- 模糊匹配不自动成功。
- 过期订单不自动成功，进入人工复核。
- 每日对账报表必须能列出：订单成功数、通知成功数、孤儿通知、人工确认单、回调失败单。

---

## 9. 开发阶段规划

### 阶段 0：合规与边界确认

目标：确定这个系统只做自有业务收款对账，还是要做对外商户支付接入。

产出：

- 业务边界说明。
- 风险清单。
- 是否接入官方微信支付 / 支付宝商户 API 的决策。

如果要对外提供支付收单或资金清算，建议直接走官方商户 / 服务商模式，不建议走个人通知监听模式。

### 阶段 1：安卓项目安全 fork

目标：得到一个可控、可编译、无第三方硬编码上报地址的 APK。

任务：

1. Fork 原 Android 项目或重建一个最小 Android 项目。
2. 删除 release APK。
3. 删除硬编码 `pay.qingsonge.com`。
4. URL 改成扫码绑定或后台下发。
5. 加 HTTPS、HMAC、nonce、timestamp。
6. 加本地队列和失败重试。
7. 加设备心跳。
8. 自行编译签名。

验收：

- APK 内 `strings classes.dex` 不再出现第三方域名。
- 断网后恢复能补发。
- 服务端能识别重复事件。

### 阶段 2：收款管理端 MVP

目标：能接收安卓通知并入库展示。

任务：

1. 建表：`nl_device`、`nl_account`、`nl_notification_event`。
2. 实现设备绑定。
3. 实现通知验签入库。
4. 实现通知列表和详情页。
5. 实现基础解析器。
6. 实现账号在线状态。

验收：

- 手机收到微信 / 支付宝通知后，后台能看到标准化流水。
- 重复上报不会重复入库。
- 错误签名请求被拒绝。

### 阶段 3：订单匹配 MVP

目标：通知能匹配待支付订单，但模糊订单不自动成功。

任务：

1. 建表：`nl_collect_session`、`nl_audit_log`。
2. EPay 插件创建收款会话。
3. 通知匹配订单。
4. 唯一匹配自动成功。
5. 多候选进入人工复核。
6. 孤儿通知可人工补单。

验收：

- 一笔订单、一条到账通知可以自动完成。
- 同金额多订单不会误完成。
- 人工处理全程有审计日志。

### 阶段 4：EPay 插件集成

目标：让 EPay 保持原下单 / 查询 / 回调体验。

任务：

1. 新增 `notifyledger` 插件。
2. 插件配置后台参数。
3. 插件 `submit()` 调用收款管理端创建会话。
4. 新增内部确认接口。
5. 内部确认接口调用 `processNotify()`。
6. 联调商户异步通知。

验收：

- 商户按原 EPay 协议下单。
- 用户支付后订单从待支付变成功。
- 商户收到 `TRADE_SUCCESS` 回调。
- 回调失败能进入 EPay 原有重试机制。

### 阶段 5：运营与风控

目标：能长期运行，出问题能定位。

任务：

1. 设备离线告警。
2. 账号异常告警。
3. 通知长时间无流水告警。
4. 订单超时关闭。
5. 回调失败重试面板。
6. 日报、月报、账号统计。
7. 数据备份和恢复演练。

验收：

- 设备掉线能及时发现。
- 每日可核对订单金额与通知金额。
- 所有异常单能查到处理过程。

---

## 10. 推荐目录结构

```text
/mnt/d/desktop/wxzf
├── Epay/                              # 现有彩虹易支付
│   └── plugins/
│       └── notifyledger/              # 新增 EPay 插件
├── notify-ledger-server/              # 新增收款管理端
│   ├── api/                           # 设备接口 / 内部接口
│   ├── admin/                         # 管理后台
│   ├── migrations/                    # 数据库迁移
│   ├── parsers/                       # 微信 / 支付宝通知解析
│   └── workers/                       # 匹配 / 回调 / 重试任务
├── android-notification-client/       # 自己 fork / 重写后的安卓端
└── docs/
    ├── 通知字段样本.md
    ├── 订单匹配规则.md
    ├── 安全审计清单.md
    └── 部署手册.md
```

---

## 11. 关键风险清单

### 技术风险

- 安卓后台保活不稳定。
- 通知文案变化导致解析失败。
- 同金额订单并发导致误匹配。
- 手机断网导致通知延迟上报。
- 用户关闭通知权限导致漏单。
- 第三方 APK 可能带硬编码上报地址或二进制差异。

### 业务风险

- 通知不是正式支付结果凭证。
- 对外提供支付接口可能涉及支付业务合规问题。
- 个人号 / 普通收款码用于商业聚合收款可能带来平台风控风险。
- 商户入驻、资金清算、反洗钱、反诈、赌博/黑灰产识别都不是单纯技术问题。

### 控制措施

- 优先官方商户支付 API。
- 通知监听仅做自有收款对账或补充校验。
- 所有自动成功规则必须保守。
- 模糊订单必须人工复核。
- 每日对账必须落地。
- 不安装未经自己编译验证的 APK。

---

## 12. 下一步建议

推荐按这个顺序推进：

1. 先把 Android 项目安全 fork，去掉硬编码第三方地址。
2. 写一个最小 `notify-ledger-server`，只接收通知入库。
3. 接一台测试手机，连续跑 24 小时验证漏通知、重复通知、断网补发。
4. 再做 EPay 插件，只跑测试商户和测试订单。
5. 最后再做统计、账号管理、人工复核和运营后台。

不要一上来直接改 EPay 核心，也不要直接把第三方 APK 装到真实收款手机上。
