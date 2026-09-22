# MiniCloudStorage

匿名临时网盘：文件保存 7 天，单文件最大 4GB，上传后用短码访问。

公网地址：https://minicloudstorage.19121122.xyz

## 隔离约定

本仓库只使用下列边界，避免和同一套 K8s / PostgreSQL / SeaweedFS 上的其它项目混数据：

| 资源 | 名称 |
| --- | --- |
| Kubernetes namespace | `minicloudstorage` |
| PostgreSQL database / role | `minicloudstorage` |
| SeaweedFS collection | `minicloudstorage` |
| SeaweedFS Filer 前缀 | `/minicloudstorage/`（`files/` 正式文件，`tmp/` 分片） |
| nginx vhost | `minicloudstorage.19121122.xyz`（独立 conf，不改其它 server） |

不要把工作负载放到 `default`，不要往公共 `postgres` 库建表，不要往 Filer 根目录写文件。

## 目录

- `backend/` Go API（分块上传、明文密码、流式下载、过期清理）
- `frontend/` Flutter Web
- `deploy/k8s/` Namespace、Quota、Deployment、CronJob
- `deploy/postgres/init.sh` 创建独立库和用户
- `deploy/nginx/` 独立 vhost；`nginx-test-pod.yaml` 把 conf 挂进现有 nginx-test

## 本机构建与部署（当前集群）

在 `k8s-master`（192.168.43.111）上：

```bash
bash deploy/postgres/init.sh
bash scripts/build.sh
bash scripts/deploy.sh
```

当前集群没有项目镜像仓库。API 是 namespace `minicloudstorage` 里的普通 Pod（ClusterIP），用 `hostPath` 挂 master 上的 `/mnt/wsl/PhysicalDrive3/in-house_project/mini_cloud_storage/dist`；镜像仍是 `alpine:3.20`，约定名 `minicloudstorage/api`，Dockerfile 已提供。PostgreSQL 在 master 本机 `192.168.43.111:5432`，不在集群里。

公网只走 Cloudflare 隧道域名；nginx 只认 Host `minicloudstorage.19121122.xyz` 和内网 `192.168.43.111`。管理维护页 `/admin/setup` 仅后者可开。

## 配置

应用通过环境变量读取连接与运行参数（见 `backend/internal/config/config.go`）。  
**本地 `.env` 与集群 Secret 是两条平行路径**，当前生产部署不依赖 `.env`。

### 应用识别的变量

| 变量 | 必填 | 默认（未设置时） | 说明 |
| --- | --- | --- | --- |
| `DATABASE_URL` | 是 | （无） | Postgres 连接串 |
| `ADMIN_PASSWORD` | 否 | 空 | 管理相关密码 |
| `LISTEN_ADDR` | 否 | `:8080` | HTTP 监听地址（集群 Deployment 中为 `:18080`） |
| `PUBLIC_BASE_URL` | 否 | `https://minicloudstorage.19121122.xyz` | 对外访问根 URL |
| `STATIC_DIR` | 否 | `/app/static` | 前端静态文件目录 |
| `SEAWEEDFS_FILER_URL` | 否 | 集群内 Filer 服务地址 | SeaweedFS Filer |
| `SEAWEEDFS_PREFIX` | 否 | `/minicloudstorage` | Filer 路径前缀 |
| `SEAWEEDFS_COLLECTION` | 否 | `minicloudstorage` | Seaweed collection |
| `SEAWEEDFS_FILE_TTL` | 否 | `8d` | 正式文件 TTL |
| `SEAWEEDFS_TMP_TTL` | 否 | `2d` | 临时分片 TTL |
| `CHUNK_SIZE` | 否 | `8388608`（8MiB） | 分块大小（字节） |
| `MAX_SIZE` | 否 | `4294967296`（4GiB） | 单文件上限（字节） |

键名示例见仓库根目录 `.env.example`。文件保留时长等业务 TTL（如 7 天）部分写在代码常量里，不是环境变量。

### 路径 A：本地开发（可选）

仅在本机直接跑进程、需要用文件喂环境变量时使用：

```bash
cp .env.example .env
# 编辑 .env，把 CHANGE_ME 等改成真实值