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