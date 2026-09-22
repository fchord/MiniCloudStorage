# MiniCloudStorage 架构与数据流

匿名临时网盘：浏览器上传文件，保存约 7 天，单文件最大 4GB，用短码访问下载。公网入口：`https://minicloudstorage.19121122.xyz`。

## 组件

| 组件 | 作用 |
| --- | --- |
| Flutter Web（`frontend/`） | 上传页、短码打开页、管理页 |
| Go API（`backend/`） | 分块上传、元数据、流式下载、过期清理、管理接口 |
| PostgreSQL | 元数据（短码、过期时间、会话等）；库/角色名 `minicloudstorage` |
| SeaweedFS Filer | 对象存储；collection / 前缀见下方隔离约定 |
| nginx | 按 Host 反代到 API；独立 vhost |
| Cloudflare | 公网隧道 / CDN，对外只暴露约定域名 |
| Kubernetes namespace `minicloudstorage` | API Deployment、Service、清理 CronJob、相关 Secret |

隔离边界（与 README「隔离约定」一致）：不要用 `default` namespace、公共 `postgres` 库或 Filer 根目录。

## 请求怎么走（公网）

```text
浏览器
  → Cloudflare（域名 minicloudstorage.19121122.xyz）
  → nginx（按 Host 反代）
  → Service/Pod：Go API（namespace minicloudstorage）
       ├─ 读/写 PostgreSQL（元数据）
       └─ 读/写 SeaweedFS Filer（`/minicloudstorage/` 下 files/ 与 tmp/）
```

## 管理维护页

/admin/setup 仅内网 Host（如 192.168.43.111）可开，不走公网域名策略。

## 上传主路径

前端发起分块上传（chunk），API 将分片落到 Filer 前缀下的 tmp/。
全部块齐后，API 在库中写入元数据（含短码、过期等），对象落到 files/。
返回短码；用户之后用短码打开下载页。
可选：上传可带访问密码（实现细节见代码；属产品能力，不改变上述分层）。

## 下载主路径（短码）

浏览器打开短码对应页面 → 调 API。
API 查 Postgres：短码是否存在、是否过期、是否需密码。
通过则从 SeaweedFS 流式读出文件内容返回。

## 过期与清理

业务上文件约保留 7 天（TTL 部分在代码常量 / Seaweed TTL 环境变量中配置）。
集群内 CronJob（deploy/k8s/app.yaml 中 cleanup）周期性跑 API 的清理模式，删过期元数据与对象。

## 部署形态（当前）

API 跑在 namespace minicloudstorage；二进制经 hostPath 挂到 master 上的 dist（见 README 部署说明）。
Postgres 在 master 本机，不在集群内。
敏感配置：DATABASE_URL、ADMIN_PASSWORD 来自 Secret；细节见 README「配置」。
一页读完即可上手改代码或排障；更细的接口列表以代码与后续文档为准。