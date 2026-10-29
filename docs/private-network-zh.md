# ifshare 私有网络部署与交付操作说明

本文用于部署由多个 ifshare 节点组成的私有网络，也可作为运维手册和交付文档的基础。适用于本项目源码构建的 ifshare，首次整理日期为 2026-10-29。升级版本后，应重新执行本文的验收步骤。

## 1. 部署约定

本项目基于 Kubo，编译后的程序名称为 `ifshare`。子命令、`IPFS_PATH` 环境变量、`swarm.key` 格式、`/ipfs/` 内容路径及 `/api/v0/` RPC 接口沿用 Kubo 的约定。

新仓库执行 `ifshare init` 后，网络相关默认配置为：

```json
{
  "Bootstrap": [],
  "AutoConf": { "Enabled": false },
  "DNS": { "Resolvers": {} },
  "Routing": { "DelegatedRouters": [] },
  "Ipns": { "DelegatedPublishers": [] }
}
```

这表示初始化不会填入公网引导节点，也不会通过 AutoConf 加载公网基础设施配置。已有仓库的配置不会因更换程序而自动清空。

组网时需同时满足：

- 所有节点使用内容完全相同的 `swarm.key`，由一台机器生成后安全分发。
- 每个节点分别初始化，保留各自的身份密钥和 Peer ID。不要将一个节点的整份 `config` 复制给其他节点。
- 节点之间能够访问对方的 Swarm TCP 端口，并配置自己的引导节点。
- 启动时设置 `LIBP2P_FORCE_PNET=1`。缺少 `swarm.key` 时应拒绝启动，避免误启动为普通节点。

空 `Bootstrap` 本身不构成私网隔离。`swarm.key` 限制 libp2p 节点之间的连接；RPC API、HTTP Gateway 和其他 HTTP 出站功能仍需按交付网络要求配置访问权限与防火墙。

## 2. 三节点示例与端口规划

下面假设三台独立主机位于同一内网。将示例 IP 替换为实际地址。

| 节点 | 内网 IP | Swarm TCP 端口 | 角色 |
|---|---|---|---|
| A | `10.20.0.11` | `4001` | 引导节点、存储节点 |
| B | `10.20.0.12` | `4001` | 引导节点、存储节点 |
| C | `10.20.0.13` | `4001` | 普通存储节点 |

引导节点也是普通节点，负责帮助其他节点找到网络，并不自动保存全网文件。配置 A、B 两个入口可以减少对单个入口的依赖。

| 端口 | 用途 | 本文配置 |
|---|---|---|
| `4001/TCP` | 节点间私网通信 | 对私网其他节点开放 |
| `5001/TCP` | RPC 管理接口 | 仅监听 `127.0.0.1` |
| `8080/TCP` | HTTP Gateway | 仅监听 `127.0.0.1` |

私网使用 TCP。本文将 Swarm 监听配置为 TCP，关闭自动端口映射和 mDNS，使用显式引导节点。

同一台主机运行多个节点时，每个节点必须使用不同的 `IPFS_PATH`，并分别设置不同的 Swarm、API、Gateway 端口，例如 `4101/5101/8181` 和 `4102/5102/8182`。本机已有节点时，不要让试验节点占用它的端口或仓库。

## 3. 编译和安装

在源码根目录执行：

```bash
make build
./cmd/ipfs/ifshare version
```

产物为 `cmd/ipfs/ifshare`，Windows 为 `cmd/ipfs/ifshare.exe`。将匹配目标操作系统和 CPU 架构的程序分发到节点主机。

开发机可执行 `make install`，安装到 `GOBIN`；未设置时安装到 `GOPATH/bin`。也可仅将构建目录加入当前终端的命令搜索路径：

```bash
export PATH="$PWD/cmd/ipfs:$PATH"
ifshare version
```

交付时记录源码提交、程序版本及程序文件的 SHA-256：

```bash
git rev-parse HEAD
ifshare version --all
openssl dgst -sha256 ./cmd/ipfs/ifshare
```

## 4. 每个节点分别初始化

以下命令在 A、B、C 上分别执行。示例使用独立目录 `$HOME/.ifshare`；生产环境可改成固定绝对路径，但初始化、密钥分发、启动服务和管理命令必须始终指向同一目录。

```bash
export IPFS_PATH="$HOME/.ifshare"
umask 077
mkdir -p "$IPFS_PATH"
ifshare init
ifshare config Identity.PeerID
```

记录各节点输出的 Peer ID，后面分别记为 `NODE_A_ID`、`NODE_B_ID`、`NODE_C_ID`。

初始化后核对：

```bash
ifshare config Bootstrap
ifshare config AutoConf.Enabled
ifshare config Routing.DelegatedRouters
ifshare config Ipns.DelegatedPublishers
ifshare config DNS.Resolvers
```

依次应为 `[]`、`false`、`[]`、`[]`、`{}`。不要使用 `autoconf-on` 或 `default-networking` 配置档，它们会启用公网自动引导配置。也不要对内网节点使用 `server` 配置档，该配置档会过滤私有 IP 地址范围。

## 5. 生成一次私网密钥并分发

在可信管理机上执行一次。该命令需要 `openssl`：

```bash
umask 077
(
  set -e
  set -C
  {
    printf '/key/swarm/psk/1.0.0/\n/base16/\n'
    openssl rand -hex 32
  } > swarm.key
)
chmod 600 swarm.key
```

`set -C` 用于防止覆盖当前目录中已有的 `swarm.key`。如果命令失败，不要分发产生的不完整文件。

文件由三行组成：

```text
/key/swarm/psk/1.0.0/
/base16/
<64 个十六进制字符，代表 32 字节随机密钥>
```

第三行必须由随机数命令生成，不能直接复制上面的占位文本。不要在各节点重新运行生成命令，也不要将真实密钥放进 Git、交付文档正文或工单日志。

通过 SSH/SCP 或现有的安全交付渠道，将同一份文件分发给 A、B、C。例如，节点 A 使用账户 `deploy` 和仓库 `/home/deploy/.ifshare` 时：

```bash
scp swarm.key deploy@10.20.0.11:/home/deploy/.ifshare/swarm.key
```

对其余节点使用相应的账户、IP 和仓库绝对路径。在每个节点上确认：

```bash
chmod 600 "$IPFS_PATH/swarm.key"
openssl dgst -sha256 "$IPFS_PATH/swarm.key"
```

三个节点的 SHA-256 摘要必须一致，文件所有者应为运行 ifshare 的账户。摘要用于核对文件一致性，实际密钥内容应单独保管。

## 6. 配置私网通信

在 A、B、C 上分别执行：

```bash
ifshare config --json Addresses.Swarm '["/ip4/0.0.0.0/tcp/4001"]'
ifshare config Addresses.API /ip4/127.0.0.1/tcp/5001
ifshare config Addresses.Gateway /ip4/127.0.0.1/tcp/8080
ifshare config Routing.Type dht
ifshare config --json AutoTLS.Enabled false
ifshare config --json Discovery.MDNS.Enabled false
ifshare config --json Swarm.DisableNatPortMap true
ifshare config Plugins.Plugins.telemetry.Config.Mode off
```

这些命令使节点通过内网 TCP 互连，关闭公网证书申请、局域网自动发现、路由器自动端口映射和遥测。新仓库已经关闭 AutoConf，不需要再次配置；已有仓库请先执行第 10 节。

使用多个网卡的主机，应确认其他节点实际能够访问配置的 IP。跨网段部署需要相应路由和防火墙规则；有相同密钥并不能解决网络不可达问题。

## 7. 添加自己的引导节点

在操作终端中，将下面的值替换为第 4 节记录的实际 Peer ID：

```bash
NODE_A_ID='替换为节点A的PeerID'
NODE_B_ID='替换为节点B的PeerID'
```

在节点 A 上添加 B：

```bash
ifshare bootstrap add "/ip4/10.20.0.12/tcp/4001/p2p/$NODE_B_ID"
```

在节点 B 上添加 A：

```bash
ifshare bootstrap add "/ip4/10.20.0.11/tcp/4001/p2p/$NODE_A_ID"
```

在节点 C 上添加 A、B：

```bash
ifshare bootstrap add "/ip4/10.20.0.11/tcp/4001/p2p/$NODE_A_ID"
ifshare bootstrap add "/ip4/10.20.0.12/tcp/4001/p2p/$NODE_B_ID"
```

每个操作终端都要设置其命令中使用的变量。地址中的 Peer ID 必须属于该 IP 上实际运行的节点，不要填写本节点的 Peer ID。完成后检查：

```bash
ifshare bootstrap list
```

输出只应包含自己维护的私网地址，不应出现 `auto` 或公共引导节点。

## 8. 启动和停止

首次验证时，在各节点独立终端前台运行。Linux 可用 `timeout` 限制试运行时间：

```bash
export IPFS_PATH="$HOME/.ifshare"
export LIBP2P_FORCE_PNET=1
timeout 120s ifshare daemon
```

日志应包含 `Swarm is limited to private network of peers with the swarm key` 和 `Daemon is ready`。试运行到期会自动结束。macOS 可使用已安装的 `gtimeout`，或前台运行并在验证结束后及时按 `Ctrl+C`。

管理命令使用同一个 `IPFS_PATH`。正常停止自己管理的节点：

```bash
ifshare shutdown
```

长期运行的 Linux 服务可参考以下 systemd 模板。使用前应已创建运行账户 `ifshare`，将程序安装到 `/usr/local/bin/ifshare`，并以该账户完成 `/data/ifshare` 仓库的初始化、配置与密钥分发。此处的 `/data/ifshare` 是生产目录示例，与前面的 `$HOME/.ifshare` 二选一，不能指向空的另一个仓库。

```ini
[Unit]
Description=ifshare private network node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ifshare
Group=ifshare
Environment=IPFS_PATH=/data/ifshare
Environment=LIBP2P_FORCE_PNET=1
Environment=IPFS_TELEMETRY=off
ExecStart=/usr/local/bin/ifshare daemon
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=120
UMask=0077

[Install]
WantedBy=multi-user.target
```

将文件保存为 `/etc/systemd/system/ifshare.service`，由管理员启用：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ifshare
sudo systemctl status ifshare
sudo journalctl -u ifshare -n 100 --no-pager
```

停止服务用 `sudo systemctl stop ifshare`。服务方式部署后，管理命令也必须以正确账户访问 `/data/ifshare`，例如 `sudo -u ifshare env IPFS_PATH=/data/ifshare ifshare swarm peers`。

## 9. 连通性和文件传输验收

### 9.1 核对节点连接

在每个节点执行：

```bash
ifshare id
ifshare swarm peers
```

确认各节点 Peer ID 不同，连接中的 Peer ID 属于本次部署清单。第一个节点单独启动且关闭 mDNS 时，连接列表为空属于正常现象，其他节点启动后再验收。

需要定位引导问题时，可显式连接一个目标节点：

```bash
ifshare swarm connect "/ip4/10.20.0.11/tcp/4001/p2p/$NODE_A_ID"
```

### 9.2 验证跨节点读取

在节点 A 上创建并添加测试文件：

```bash
printf 'ifshare private network acceptance\n' > acceptance.txt
ifshare add -Q acceptance.txt
openssl dgst -sha256 acceptance.txt
```

记录输出 CID。在 B 和 C 上分别填写该 CID：

```bash
TEST_CID='替换为节点A输出的CID'
ifshare cat --timeout=30s "$TEST_CID" > acceptance.received.txt
openssl dgst -sha256 acceptance.received.txt
```

收到的文件内容和 SHA-256 应与 A 一致。需要由 B、C 长期保存副本时，分别执行：

```bash
ifshare pin add --timeout=60s "$TEST_CID"
ifshare pin ls "$TEST_CID"
```

引导节点配置不会自动复制或备份文件；`cat` 获取的数据可能只是缓存。持久保存要有明确的 pin 或上层复制策略。

### 9.3 验收清单

- [ ] 各节点使用交付指定的 ifshare 版本和程序摘要。
- [ ] 各节点 Peer ID 不同，`swarm.key` 摘要相同，权限和所有者正确。
- [ ] `AutoConf.Enabled=false`，`Bootstrap` 只包含私网节点。
- [ ] TCP 监听地址、RPC/Gateway 监听地址符合端口规划。
- [ ] 守护进程启用 `LIBP2P_FORCE_PNET=1`，启动日志显示私网模式。
- [ ] B、C 能读取 A 添加的文件，内容摘要一致。
- [ ] 需要持久保存的节点已 pin 验收 CID。
- [ ] 重启一个节点后，仍使用原 Peer ID、密钥、配置和 pin 数据。
- [ ] 隔离测试仓库中缺少密钥时，强制私网启动失败；使用不同密钥时不能连接业务私网。此项不通过修改运行中的业务节点密钥来测试。

## 10. 已有仓库迁移到私网

更换程序不会自动删除旧仓库里的公网配置。先确认 `IPFS_PATH`，停止该节点，并备份仓库；备份包含身份私钥，按密钥材料管理。

在停止状态下执行：

```bash
ifshare config profile apply autoconf-off
```

该配置档会清空原有 Bootstrap、DNS resolver、delegated router 和 IPNS delegated publisher 配置，包括自行配置的值。执行前记录需要保留并重新设置的私有端点。

然后按第 5～8 节放置共同密钥、配置 TCP、添加私有引导节点并以强制私网模式启动。如果曾使用 `server` 配置档，可先执行 `ifshare config profile apply local-discovery` 撤销它附加的默认私网地址过滤，再执行第 6 节关闭 mDNS 和自动端口映射。自定义的 `Swarm.AddrFilters`、`Peering.Peers`、`Routing.Type=custom` 及环境变量覆盖仍需根据原部署逐项核对。

迁移不会自动删除已有内容，也不会更换节点身份。不要通过删除仓库或重复初始化来清除引导节点。

## 11. 常见故障

| 现象 | 检查与处理 |
|---|---|
| 提示强制私网但未配置私网 | 检查启动进程的 `IPFS_PATH`，确认该目录存在有效 `swarm.key` |
| 节点无法握手 | 比较密钥 SHA-256；确认三行格式、64 位十六进制密钥及文件权限 |
| AutoConf 与私网冲突 | 对旧仓库应用 `autoconf-off`，重新添加私有引导节点 |
| AutoTLS 与私网冲突 | 设置 `AutoTLS.Enabled=false`，使用 TCP 监听 |
| 连接列表一直为空 | 检查目标进程、IP、端口、防火墙、Peer ID；尝试显式 `swarm connect` |
| 内网 IP 被拒绝 | 检查是否应用了 `server` 配置档，以及 `Swarm.AddrFilters` 是否阻止私有地址 |
| `address already in use` | 同机多节点使用不同端口和仓库，不要关闭不属于本次部署的进程 |
| RPC 请求连不上或操作错节点 | 对照 `IPFS_PATH`、`Addresses.API`，必要时使用 `--api=/ip4/127.0.0.1/tcp/实际端口` |
| 能互连但 CID 获取超时 | 确认提供方在线且保存该 CID；先测试直接连接和小文件，再排查路由与提供记录 |
| 文件重启后或 GC 后消失 | 确认负责保存副本的节点执行了 `pin add`；仅建立连接或读取文件不保证长期保留 |

## 12. 运维、升级与交付记录

新增节点按“单独初始化 → 分发已有 `swarm.key` → 配置自己的 Peer ID 和引导入口 → 验收”的顺序进行，不生成新的全网密钥。

密钥泄露或需要轮换时，安排维护窗口，生成一份新密钥并协调所有节点切换、重启和验收。新旧密钥节点不能直接互连，轮换不会自动兼容两个密钥。备份密钥、仓库及配置；交付文档只记录保管位置和负责人，不附真实私钥。

升级使用本项目构建的 ifshare 包，记录源码提交、版本、文件摘要与仓库迁移情况。不要将旧版二进制直接作为仓库升级后的回滚方案；先核对该版本对仓库版本的支持。

可将下面的记录表复制到交付文档：

| 记录项 | 实际值 |
|---|---|
| 项目/环境名称、交付日期 | 待填写 |
| 源码提交、ifshare 版本、程序 SHA-256 | 待填写 |
| 节点名称、主机 IP、Peer ID | 每个节点分别填写 |
| 运行账户、程序路径、`IPFS_PATH` | 每个节点分别填写 |
| Swarm/API/Gateway 地址 | 每个节点分别填写 |
| 私有引导节点清单 | 待填写 |
| 密钥摘要、保管位置、保管负责人 | 待填写，不填写实际密钥 |
| 启停方式、服务名称、日志位置 | 待填写 |
| 验收 CID、文件摘要、各节点 pin 状态 | 待填写 |
| 备份、恢复、密钥轮换责任人 | 待填写 |

## 13. 源码和配置参考

- [初始化配置](../config/init.go)：新仓库空 Bootstrap 和关闭 AutoConf 的默认设置。
- [初始化与私网引导集成测试](../test/cli/init_test.go)：初始化默认值、手动引导和共同密钥下的跨节点读取。
- [配置参考](config.md)：Bootstrap、AutoConf、Routing、Discovery 和网络监听配置。
- [私网功能说明](experimental-features.md#private-networks)：`swarm.key` 和 TCP 私网限制。
- [遥测配置](telemetry.md)：遥测开关及环境变量优先级。
