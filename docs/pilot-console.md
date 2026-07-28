# GCMS 网页端 Pilot 控制台

## 目标和边界

Pilot 控制台让已登录 GCMS 的用户直接用桌面或手机浏览器控制自己的 Pilot。绑定对象是“Pilot 设备 + 已导入的 GCMS 技能包连接”，不是某个站点。

- Pilot 只向 GCMS 建立出站 HTTPS 连接/可靠长轮询，不监听本地端口。
- 不要求公网访问 Pilot，不使用 SSH，不依赖 Cloudflare、DNS 或 Caddy。
- `gcmsp_` 继承平台密钥的实时成员范围和 scopes；`gcms_` 固定为原单站。绑定不能扩权。
- 默认站点只用于新对话，不等于授权范围。
- 后台密码只提交给 GCMS，不发送给 Pilot、不写入对话、不保存。

## 架构

```text
手机/桌面浏览器（后台 session + CSRF）
  └─ GCMS：设备、授权站点、任务、事件、一次性解锁
       └─ system.db：binding / lease / request_id / event / unlock / audit
            ↑ 出站 HTTPS 心跳和长轮询
       Pilot 后台服务
         ├─ 既有 ConvStore → create_conversation → agent::run_turn
         ├─ 既有 TaskStore（tasks.json）
         └─ 既有 ManagedStore（managed.json）
```

远程对话直接使用 Pilot 现有 `Conversation`、模型、技能包工作目录、权限钩子和任务执行体系，所以会出现在本地侧边栏；没有单独的假会话数据库。

## 设备绑定协议

1. Pilot 在“连接与模型”中选择 `kind=gcms` 的技能包连接。
2. Pilot 生成稳定 UUID 设备 ID 和 256-bit 设备密钥。
3. 设备密钥保存在 macOS Keychain 或 Windows Credential Manager；原技能包 key 仍在自己的安全存储条目。
4. Pilot 用原 `gcmsp_` / `gcms_` Bearer key 调用 `POST /api/platform/v1/pilot/bindings`。
5. GCMS 验证原 key 和默认站点，只保存设备密钥 SHA-256。
6. 后续设备请求使用 `X-GCMS-Pilot-Device` + 设备 Bearer secret。
7. GCMS/Pilot 任一端解绑都会撤销 binding、取消未完成任务并删除解锁；设备撤销会替换 secret hash，旧凭据立即 401。

协议版本当前为 `1`。不兼容返回 HTTP 426 和 `protocol_incompatible`，Pilot 会停止领取并提示升级。

## API 和任务协议

设备接口：

- `POST /api/platform/v1/pilot/bindings`
- `POST /api/platform/v1/pilot/heartbeat`
- `POST /api/platform/v1/pilot/tasks/claim`
- `POST /api/platform/v1/pilot/tasks/{id}/events`
- `POST /api/platform/v1/pilot/tasks/{id}/confirmation`
- `PATCH /api/platform/v1/pilot/tasks/{id}`
- `PATCH /api/platform/v1/pilot/bindings/{id}/default-site`
- `DELETE /api/platform/v1/pilot/bindings/{id}`

网页接口位于 `/admin/pilot/api/`，使用后台 session；写请求必须携带 `X-CSRF-Token`。

正式状态为 `queued`、`claimed`、`running`、`waiting_confirmation`、`completed`、`failed`、`canceled`、`expired`。设备离线时任务保持 queued。

`(binding_id, request_id)` 唯一。同一 canonical 输入重放返回原任务；不同输入复用 ID 返回 `idempotency_conflict`。Pilot 还会按本地 conversation ID 拦截重复投递。

领取在 SQLite 短事务中完成，GCMS 只保存 lease token 哈希。`claimed` 租约过期可重新领取；已经 `running` 的写任务不会被另一进程自动重跑，而会成为 `execution_interrupted`，只能由用户显式重试并产生新 request_id。

任务事件有单调 `seq` 和唯一约束，网页可按事件 ID 游标补取。断线不依赖内存流。

心跳还会按技能包连接上报该连接对应的定时任务和托管快照。GCMS 只把快照用于展示，不把它当作另一份可独立修改的数据；网页的新增、暂停、恢复、删除和关闭操作仍进入同一远程任务队列，由 Pilot 校验连接归属后写回现有 `TaskStore` / `ManagedStore`，下一次心跳再回传权威状态。设备离线时操作明确保持排队。

Pilot 现有工具权限闸门产生待确认请求时，任务进入 `waiting_confirmation`。网页的允许/拒绝决定只绑定当前 permit ID；Pilot 收到决定后写入现有 permit 响应文件再继续或拒绝执行。等待期间取消仍会终止本地 Run。

## 表和迁移

平台启动时幂等创建：

- `pilot_devices`
- `pilot_bindings`
- `pilot_tasks`
- `pilot_task_events`
- `pilot_conversations`
- `pilot_unlocks`
- `pilot_audit_logs`
- `pilot_binding_snapshots`

online 不持久化成容易卡死的布尔值，而由最近 45 秒心跳派生。默认站点失效不会静默改成首项。

## 站点规则

新执行的选择优先级：

1. 历史 Conversation 自身 `site_slug/site_slugs`；
2. 用户本次明确选择；
3. binding 默认站点；
4. 当前授权列表第一项。

历史多站列表原样保留。默认站点失效只告警并临时回退，不覆盖持久值。GCMS 在创建和领取时都会重算原 key 的实时权限，不相信前端传来的 scopes 或站点范围。

## 确认、解锁与审计

只读操作不重复要求密码。普通写任务必须由网页明确确认。

失败任务可重试，但会生成新的 request_id 和本地 Conversation；敏感/破坏性任务重试必须重新输入密码并消费一枚新的单用途解锁令牌。

删除、改域名、发布、覆盖内容等敏感/破坏性操作：

1. 用户在 GCMS 网页重新输入后台密码；
2. GCMS bcrypt 验证后立即丢弃密码；
3. 签发随机短时 token，数据库仅存哈希；
4. token 绑定用户、设备、binding、目标站点、操作、request_id、过期时间；
5. 创建对应任务时一次性消费，错配、过期和复用都拒绝；
6. 签发、拒绝、任务和解绑写入审计。

旧 Pilot 本地解锁接口为兼容既有功能继续保留；网页远程操作使用新的窄授权，两者不能互换。

## 使用

### Pilot

导入 GCMS 技能包 → 打开“连接与模型” → 点击“绑定 Pilot 控制台” → 可填写设备名和默认站点。解绑只移除控制台绑定，保留技能包、API key 和历史对话。重新绑定会产生新 binding。

### 手机浏览器

登录 GCMS → 顶部“Pilot 控制台”。交互与 Pilot 保持同一心智模型：左侧是“新对话 / 定时任务 / 托管”和历史对话；手机端对应为底部主导航。设备与技能包在页头选择，站点只在新对话或定时任务中作为上下文选择，不再单独复制一套站点管理。

默认优先显示已有历史会话；进入会话后在底部直接继续输入。排队、执行、确认、失败和最终输出显示在对应会话或控制卡片中，不另设脱离上下文的通用任务中心。

站点的内容、SEO、品牌素材、主题、新建和部署检查均通过远程对话复用现有技能包能力。单站包不能访问其他站点。

## 故障处理

- **Pilot 离线**：任务保留 queued；启动 Pilot 后自动领取。request_id 防止重复点击。
- **GCMS 不可访问**：Pilot 展示真实错误并指数退避，网络恢复后重连。
- **技能包失效/站点授权移除**：心跳和领取失败，任务不会越权执行；更新技能包后重绑。
- **默认站点被删除**：提示并临时回退，不改持久默认值。
- **模型不可用**：任务明确 failed；完成模型安装/登录后重试。
- **执行中退出**：租约过期后标记 `execution_interrupted`，不会自动重复写。
- **重复投递/断线**：服务端 request_id、本地 conversation ID 和事件游标共同防重、补传。
- **取消**：GCMS 标记 cancel request，Pilot 终止对应运行并回报 canceled。
- **等待确认过期**：进入 expired，必须重新发起。
- **版本不兼容**：升级 GCMS/Pilot；界面不会假装在线或成功。

## 安全说明

- 生产绑定只接受 HTTPS。
- 服务端不存设备明文 secret；Pilot 不把 secret 暴露给 WebView。
- 密码永不发送给 Pilot。
- task/event/output 不记录密码、技能包 key、设备 secret 或 unlock token。
- 默认站点不是权限边界。
- Pilot 控制台与 SSH、Cloudflare、DNS、Caddy 和远程服务器登录完全分离。

## 测试和验收

- Go：迁移、secret 哈希、解绑、心跳、离线排队、租约、幂等、事件、取消/重试/超时、单用途 unlock、平台/单站范围。
- Rust：旧数据兼容、API 地址、Keychain/Windows Credential Manager、心跳/退避、重复对话防重、真实 ConvStore、事件/结果同步。
- Svelte：`npm run check`；绑定卡的在线、离线、错误和默认站点。
- 响应式：360×800、390×844、1280×800；底栏、触控目标、站点选择、长输出和横向溢出。
- 平台：macOS 原生 `cargo test`；Windows MSVC runner 执行 `cargo test --target x86_64-pc-windows-msvc`。

验收闭环：

```text
Pilot 绑定技能包 → GCMS 显示在线 → 手机选设备/站点 → 创建远程对话
→ Pilot 本地侧边栏出现并真实执行 → GCMS 增量显示结果
→ 敏感操作由 GCMS 网页密码一次性解锁 → 解绑后旧凭据失效
```
