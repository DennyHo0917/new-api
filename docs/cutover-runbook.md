# API Route 切流与回滚手册

本手册只覆盖切流操作、观察和回滚。数据库迁移、白名单快照、域名和支付实测仍按 `todolist.md` 执行。

## 切流前

1. 生成 24 小时内的 PostgreSQL 自定义格式备份，并实际恢复到隔离库验证。
2. 执行 `sh scripts/cutover-preflight.sh`，必须全部 PASS。
3. 在 VPS 执行 `sh /data/api-route/source/scripts/cutover-rollback.sh prepare`，保留当前健康后端为 `api-route-backend:rollback`。
4. 记录当前 Vercel Production Deployment URL，并确认上一份可用 Deployment 仍可 Promote。
5. 保存当前 `vercel.json` 和 VPS Compose 配置的只读校验值。

## 回滚触发条件

出现任一情况立即回滚，不继续观察：

- 管理 API 或转发 API 连续 2 分钟不可用；
- 5xx 持续超过 5%，或出现错误扣费、重复充值、余额异常；
- 旧 Key 被普通限流误判为额度耗尽；
- 登录、注册或 Session 大面积失败；
- PostgreSQL、Redis 或后端容器非 healthy 或发生重复重启。

## 回滚顺序

1. 在 Vercel 项目 Deployments 中选择切流前记录的 Production Deployment，执行 Promote；不要修改域名。
2. 若后端版本也需要回退，在 VPS 执行：

   ```sh
   sh /data/api-route/source/scripts/cutover-rollback.sh backend
   ```

3. 执行 `status` 与 `cutover-preflight.sh`，确认旧 SubRouter 路径、管理 API 和三个容器恢复。
4. 不恢复数据库备份，除非确认发生了不可逆 schema/data 破坏。普通应用回滚不得覆盖用户余额和充值记录。

## 切流后观察

在第 5、15、30、60 分钟执行：

```sh
WINDOW_SECONDS=300 sh /data/api-route/source/scripts/cutover-observe.sh
```

重点核对：HTTP 5xx/429/超时、登录迁移、SubRouter 代理与额度耗尽、消费/退款/充值数量和额度、PostgreSQL 连接数、Redis 客户端、容器健康与重启次数。

## 支付上线策略

首次切流默认关闭未完成真实端到端验证的支付方式。只有完成创建订单、签名/链上验证、成功到账、重复回调、失败和过期场景后才启用。加密充值还必须确认专用收款钱包、Arbitrum RPC、TronGrid Key，并处理现存 pending 订单；否则保持关闭。
