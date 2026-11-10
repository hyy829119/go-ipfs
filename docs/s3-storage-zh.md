# IFShare 本地存储与 S3 对象存储

## 存储模式

使用 `--storage=local` 或 `--storage=s3` 选择新仓库的存储方式。不传参数时，保留原来的本地存储行为。

| 数据 | `local` | `s3` |
| --- | --- | --- |
| 文件内容块、目录 DAG 块 | 仓库的 `blocks/` | S3 桶内的节点独立前缀 |
| 节点身份、配置、`swarm.key` | 本地仓库 | 本地仓库 |
| Pin 状态、MFS 根引用及节点运行状态 | 本地 `datastore/` | 本地 `datastore/` |

S3 存储的是 IFShare 内容块，并不是按原始文件名保存的一整份文件。文件上传、CID、WebUI 文件列表、预览、下载和节点之间的内容传输继续使用现有接口。需要同时备份本地仓库和对象存储；仅备份 S3 桶无法恢复节点身份及 WebUI 的 MFS 路径。

存储模式在初始化时写入配置，以后 `ifshare daemon` 自动读取。启动时也可加同一个 `--storage` 参数进行核对；如果参数与已有仓库不一致，会拒绝启动。更换后端需要建立新仓库并迁移数据，本功能不执行原地迁移，也不会在 S3 故障时偷偷写到本地。

## 启动本地 MinIO 容器

需要启动 Docker，安装 Python 3、Go 和 Make。在 Kubo 项目根目录执行：

```bash
python3 dev/minio/local.py start
```

脚本从 MinIO 官方源码的固定版本 `v0.0.0-20260212201848-7aac2a2c5b7c` 构建本地镜像，避免依赖不可拉取的官方容器标签。第一次运行需要联网下载源码、Go 工具链及依赖；后续复用镜像、容器和数据卷。

| 项目 | 默认值 |
| --- | --- |
| S3 API | `http://127.0.0.1:19000` |
| MinIO Console | `http://127.0.0.1:19001` |
| 容器 | `ifshare-minio` |
| 持久数据卷 | `ifshare-minio-data` |
| 桶 | `ifshare` |
| 登录账户及随机密码 | `dev/minio/.local/credentials.json` |
| IFShare 环境配置 | `dev/minio/.local/ifshare-s3.env` |

端口仅绑定本机回环地址。脚本检查端口是否可用，凭据文件权限设为 `0600`，`.local/` 不进入 Git。开发容器使用本地生成的管理账号；生产节点应分配只允许访问自己的前缀的 S3 账户。

首次创建时可以指定端口：

```bash
python3 dev/minio/local.py start --port 19010 --console-port 19011
```

已存在的容器必须使用原来的端口。停止不会删除数据卷或密码：

```bash
python3 dev/minio/local.py stop
```

## 初始化 IFShare

构建程序：

```bash
make build
```

本地模式示例：

```bash
export IPFS_PATH="$PWD/dev/minio/.local/repo-local"
./cmd/ipfs/ifshare init --storage=local
```

S3 模式示例，使用另一个全新的本地仓库目录：

```bash
source dev/minio/.local/ifshare-s3.env
export IPFS_PATH="$PWD/dev/minio/.local/repo-s3"
./cmd/ipfs/ifshare init --storage=s3
```

连接其他 S3 服务时，在初始化前配置：

```bash
export IFSHARE_S3_ENDPOINT='https://s3.example.internal'
export IFSHARE_S3_BUCKET='ifshare'
export IFSHARE_S3_REGION='us-east-1'
export IFSHARE_S3_CREDENTIALS_FILE='/absolute/path/credentials.json'
# 可选：默认使用 nodes/<PeerID>，每个节点必须独占一个前缀。
# export IFSHARE_S3_PREFIX='nodes/node-a'
./cmd/ipfs/ifshare init --storage=s3
```

凭据文件格式如下，不要把真实凭据写进仓库的 `config`：

```json
{"accessKey":"YOUR_ACCESS_KEY","secretKey":"YOUR_SECRET_KEY"}
```

```bash
chmod 600 /absolute/path/credentials.json
```

也可以不设置凭据文件，改为在 IFShare 进程环境中设置 `AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY` 和可选的 `AWS_SESSION_TOKEN`。凭据在仓库打开时读取，轮换后需重启进程。配置中只保存凭据文件路径，WebUI 配置编辑器和 `config show` 不返回密钥。

桶必须已经存在；初始化会验证凭据、桶访问及前缀归属。S3 服务需支持路径式访问、条件写入 `If-None-Match: *` 和读取/列表的写后可见性。HTTPS 使用操作系统信任的 CA。

## 启动与验证

本机可能已有节点使用 `4001`、`5001`、`8080`。测试仓库使用另外的端口：

```bash
./cmd/ipfs/ifshare config --json Addresses.Swarm '["/ip4/127.0.0.1/tcp/14101"]'
./cmd/ipfs/ifshare config Addresses.API /ip4/127.0.0.1/tcp/15101
./cmd/ipfs/ifshare config Addresses.Gateway /ip4/127.0.0.1/tcp/18181
./cmd/ipfs/ifshare daemon --storage=s3
```

在另一个终端设置相同的 `IPFS_PATH` 后验证：

```bash
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 add -Q /path/to/test-file
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 files cp /ipfs/实际CID /test-file
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 files ls /
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 cat 实际CID
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 repo stat
./cmd/ipfs/ifshare --api=/ip4/127.0.0.1/tcp/15101 shutdown
```

在 MinIO Console 查看 `ifshare/nodes/<PeerID>/blocks/`，可以看到对象；S3 仓库不会建立本地 `blocks/` 文件夹。重启后检查原 CID、Pin 和 MFS 文件仍可读取。WebUI 仍连接该节点 RPC，无需直接连接 MinIO。

新仓库也支持 `ifshare daemon --init --storage=s3`。生产环境的服务管理方式、私有网络密钥、Apache HTTPS 和 RPC/网关反向代理沿用原部署方案；S3 模式额外需要节点到对象存储的网络访问及运行凭据。

## 性能与运行约束

- 使用 AWS SDK v2 的连接池和请求签名，批量提交默认最多 `16` 个请求并发，避免每写一个块都先发一次 HEAD。
- 对象列表每页最多 `1000` 个，按键升序遍历与垃圾回收不需要一次装入全部对象键；键查询不下载对象内容。
- 批量操作完成、失败或取消后释放持有的块缓冲。内存主要由当前导入批次、块大小和并发数决定，不按整个原文件大小分配。
- 节点占用空间以对象大小求和，默认缓存 `30s`，避免 WebUI 反复扫描整个桶；刚写入的数据可能延迟出现在空间统计中。删除会使缓存失效。
- 保持现有 `Datastore.StorageMax` 和 GC 行为；对象存储容量较大时按实际配额设置 `StorageMax`，并为统计缓存保留余量。启用桶版本管理后，GC 删除当前对象并不删除历史版本。
- S3 的网络时延、带宽、对象请求性能和计费方式决定实际吞吐；此模式不会把远端存储变成与本地 SSD 相同的延迟。

可以在停止守护进程后调整 `Datastore.Spec` 中 S3 挂载的 `concurrency`（`1`–`128`）、`requestTimeout`（默认 `30s`）和 `usageCacheTTL`（默认 `30s`，`0s` 禁用缓存），重启生效。默认值是本功能的起点，调大并发前应测量节点内存及 S3 服务负载。完整字段见 [S3 配置参考](datastores.md#s3ds)。

同一个桶可以容纳多个节点，但前缀不能相同，也不能互相包含。前缀控制对象会拒绝另一身份占用同一前缀；即使复制了同一节点身份，也禁止两个进程共享对象前缀。各节点 Pin/MFS 状态独立，网络互通并不自动同步 WebUI 文件列表。

## 自动化验证

在项目根目录执行：

```bash
make mod_tidy
go fmt ./...
make build
go test -race ./plugin/plugins/s3ds -timeout=3m
source dev/minio/.local/ifshare-s3.env
IFSHARE_TEST_S3=1 go test ./test/cli -run 'Test(StorageLocalAndValidation|S3Storage|S3DaemonInit)$' -timeout=3m
```

单元测试覆盖 datastore 契约、分页、失败传播、并发上限、内存释放、前缀隔离及凭据规则。MinIO 集成测试使用临时仓库和随机端口，覆盖上传、CID 一致性、MFS、重启、Pin、GC、网关 Range 和跨节点下载；结束后只删除测试新建的对象前缀，不删除其他数据。

性能对比使用 `64 MiB` 随机数据，保持 CID v0 和 `256 KiB` 分块，每个配置运行一次。计时包括导入和 Pin，不含节点启动及下载校验；下载后比对 SHA-256，并检查两种并发配置生成相同 CID。

```bash
IFSHARE_TEST_S3=1 go test ./test/cli -run '^$' -bench '^BenchmarkS3Import$' -benchtime=1x -timeout=4m
```

2026-11-10 在 Apple M3 Max、macOS arm64、Docker 本机 MinIO 上的两轮结果（十进制 `MB/s`）：

| 批量并发 | 第一轮 | 第二轮 |
| --- | --- | --- |
| `1` | `108.55 MB/s` | `105.07 MB/s` |
| `16` | `208.46 MB/s` | `183.23 MB/s` |

这组小规模本机测试用于确认并发写入有效，不代表网络 S3、长期运行或生产集群的吞吐承诺。部署前应使用实际网络、文件大小和请求负载重新测量。

参考：[ipfs/go-ds-s3](https://github.com/ipfs/go-ds-s3)、[AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2)、[MinIO](https://github.com/minio/minio)。
