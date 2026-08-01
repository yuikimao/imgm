# imgm — 内网镜像迁移工具

用于将容器镜像从可访问外网的机器迁移至无法访问外网 registry 的内网机器。

执行流程分为三步:**本机拉取 → 打包上传 → 远端导入**。机器地址与 SSH 账号仅需配置一次,后续以单条命令完成分发。

```bash
imgm pull -e prod nginx:1.25
```

## 目录

- [安装](#安装)
- [快速开始](#快速开始) —— 配置环境 → 验证连通性 → 分发镜像
- [三个分发命令](#三个分发命令) —— `pull` / `build` / `push` 的区别
- [预览待执行的操作](#预览待执行的操作)
- [对远程机器的操作范围](#对远程机器的操作范围)
- [常用操作](#常用操作) —— 查看配置、增减机器、变更凭据、多环境分发、导出 tar
- [两个常见问题](#两个常见问题) —— 架构不匹配、k8s 需分发至每个节点
- [经跳板机访问内网机器](#经跳板机访问内网机器)
- [补充说明](#补充说明)
- [**命令参考**](#命令参考) —— 全部命令与参数的完整说明
- [配置文件](#配置文件) —— YAML 结构与取值优先级
- [已知限制](#已知限制)

## 安装

```bash
go build -o imgm . && cp imgm "$(go env GOPATH)/bin/"
```

`~/go/bin` 通常已在 PATH 中,无需 sudo。修改代码后需重新执行该命令,否则运行的仍是旧版本;可用 `which -a imgm` 确认环境中只有一份二进制。

环境要求:

- 本机安装 `docker`
- 目标机安装 `docker`,或 `containerd`(k8s 节点)

## 快速开始

### 第 1 步:配置环境

```bash
imgm init
```

向导会依次询问以下参数:

```
环境名 (如 prod / test): prod
环境类型 [docker, k8s] (docker): k8s
目标架构 [linux/amd64, linux/arm64] (linux/amd64):
containerd 命名空间 (k8s.io):
目标机器 (逗号分隔, 支持 10.0.0.11-14, 空行结束): 10.0.1.11-13

如果这些机器只能经其中一台中转, 输入那一台的地址 (直接回车表示都能直连)
  可选: 10.0.1.11, 10.0.1.12, 10.0.1.13
跳板机:
SSH 用户 (root):
SSH 端口 (22):
SSH 密码 (直接回车改用密钥认证):
```

括号内为默认值,**直接回车即采用默认值**。

关于认证方式:内网机器多以密码认证为主,因此向导优先询问密码(不回显,需输入两次)。**在密码这一步直接回车即切换为密钥认证**,随后会询问私钥路径:

```
SSH 密码 (直接回车改用密钥认证):

使用密钥认证。此处填写本机私钥路径, imgm 仅读取该文件。
前提: 对应公钥需已配置在目标机的 ~/.ssh/authorized_keys 中。
本机私钥路径 (~/.ssh/id_rsa):
```

此处填写的是**本机**私钥,imgm 仅读取该文件用于认证。目标机侧的公钥需事先配置,imgm 不会代为安装(其对远程机器仅有查看权限):

```bash
ssh-copy-id -i ~/.ssh/id_rsa.pub root@10.0.1.11   # 每台机器执行一次
```

环境创建完成后会输出后续可执行的命令:

```
✔ 已创建环境 prod (k8s, linux/amd64, 3 台机器)

下一步:
  imgm env check prod                 # 1. 验证所有机器可连接
  imgm pull -e prod nginx:1.25        # 2. 从 registry 拉取并分发
  imgm build -e prod -t myapp:1.0 .   # 或: 构建本地应用并分发
  imgm push -e prod myapp:1.0         # 或: 分发本机已有镜像
  拉取与构建均按环境架构 linux/amd64 进行, 与本机架构无关。

机器管理 (账号默认继承本环境配置):
  imgm host ls -e prod                 # 查看机器列表
  imgm host add 10.0.1.20 -e prod      # 添加单台
  imgm host add 10.0.1.20-23 -e prod   # 批量添加 (20,21,22,23)
  imgm host rm 10.0.1.20 -e prod       # 移除机器

账号配置 (机器级设置优先于环境级):
  imgm env set prod --password '<密码>'                 # 修改本环境默认账号
  imgm host add 10.0.1.20 -e prod --password '<密码>'   # 添加时单独指定
  imgm host set 10.0.1.20 -e prod --password '<密码>'   # 为已有机器单独设置
  imgm host set 10.0.1.20 -e prod --password ''         # 清除单独设置, 恢复继承

当前为直连模式: 每台机器都从本机直接连接。
若部分机器只能经其中一台中转, 设置跳板机:
  imgm env set prod --jump <跳板机地址>
```

示例中的地址取自环境内已有机器的网段,而非固定的 `10.0.0.x`。

「环境」的定义:一组机器 + 一套 SSH 账号 + 一个目标架构。配置完成后通过 `-e prod` 引用。

跳板机相关:机器数量少于两台时不会询问。全部可直连时回车跳过,详见[「经跳板机访问内网机器」](#经跳板机访问内网机器)。

### 第 2 步:验证连通性

```bash
imgm env check prod
```

```
环境 prod  type=k8s  platform=linux/amd64  ns=k8s.io  3 台机器

10.0.1.11:22 (root)
  ✔ SSH 连接        124ms
  ✔ 运行时 ctr      1.7.11
  ✔ 命名空间        k8s.io 可访问
  ✔ 临时目录        /tmp 可写
  ✔ 剩余空间        48.2 GiB
  ✔ 架构           x86_64 → linux/amd64
10.0.1.12:22 (root)
  ✘ SSH 连接        dial tcp 10.0.1.12:22: i/o timeout

汇总: 3 台, 通过 2, 失败 1
```

**建议在分发前先执行此命令**。SSH 凭据错误、目标机缺少 containerd、磁盘空间不足等问题均会在此暴露,避免在数 GB 数据传输完成后才失败。

### 第 3 步:分发镜像

```bash
imgm pull -e prod nginx:1.25 redis:7.0
```

镜像名以空格分隔,数量不限。上传前会列出镜像、环境与全部目标机地址供确认。

传输过程带进度显示:

```
-- [prod] 机器 root@10.0.1.11:22
  上传 /tmp/.../imgm-linux-amd64-d1bbf192.tar -> /tmp/imgm-linux-amd64-d1bbf192.tar (70.5 MiB)
  [===========             ]  47.1%  33.2 MiB / 70.5 MiB  10.4 MiB/s  剩余 3s
  已传 70.5 MiB, 耗时 7s, 平均 10.4 MiB/s
  导入: docker load -i '/tmp/imgm-linux-amd64-d1bbf192.tar'
```

速率为**瞬时值**(滑动平均),可反映链路当前的实际状况,不会被传输初期的高速阶段拉平。速率过低时剩余时间显示为 `--`。输出重定向至文件时自动改为每 5 秒一行,避免回车符污染日志。

**多个镜像会合并为单个 tar**,因此上传只发生一次。建议将待迁移的镜像合并为一批发送,可显著减少数 GB 级别的重复传输。

分发成功后会输出在目标机上的确认命令:

```
#### 汇总: 机器总数 3, 成功 3, 失败 0 ####

在目标机上确认: docker images nginx:1.25
```

k8s 环境下对应输出 `ctr -n k8s.io images ls | grep nginx:1.25`。imgm 不会代为远程查询(对远程机器仅有查看权限,不执行额外探测)。

## 三个分发命令

三者的差异仅在于**镜像来源**,后续的打包、上传、导入流程完全一致。

| 命令 | 镜像来源 | 适用场景 |
|---|---|---|
| `imgm pull` | 从 registry 拉取 | 迁移第三方镜像,如 nginx、mysql |
| `imgm build` | 本机现场构建 | 迁移自研应用 |
| `imgm push` | 本机 docker 中已有 | 镜像已存在于本地,无需重新拉取 |

```bash
imgm pull  -e prod nginx:1.25 redis:7.0
imgm build -e prod -t myapp:1.0 .
imgm push  -e prod myapp:1.0
```

三者均需通过 `-e` 指定目标环境,且**产物架构完全由目标环境的 `platform` 决定,与本机架构无关** —— 在 Apple Silicon 的 Mac 上执行,发往 `linux/amd64` 环境时构建和拉取的仍是 amd64 镜像。

`build` 的 `-e` 因此不需要手写 `--platform`;指定多个架构不同的环境时,会按各自的架构分别构建:

```bash
imgm build -e prod -t myapp:1.0 .            # 按 prod 的架构构建, 构建后分发到 prod 全部机器
imgm build -e prod,staging -t myapp:1.0 .    # 架构不同则各构建一次, 相同则只构建一次
```

**不带 `-e` 时仅在本机构建,不做任何分发**,此时目标架构缺省为 `linux/amd64`,需要其他架构须显式指定 `--platform`。该模式用于导出 tar 手动迁移,详见[「仅导出 tar 手动迁移」](#仅导出-tar-手动迁移)。

## 预览待执行的操作

添加 `--dry-run` 可仅打印将要执行的命令,不产生任何数据传输:

```bash
imgm pull -e prod nginx:1.25 redis:7.0 --dry-run
```

```
#### 环境 [prod] type=k8s platform=linux/amd64 机器数=3 ####
  [dry-run] docker pull --platform linux/amd64 nginx:1.25
  [dry-run] docker pull --platform linux/amd64 redis:7.0
  [dry-run] docker save -o /tmp/.../imgm-linux-amd64-839f0af5.tar --platform linux/amd64 nginx:1.25 redis:7.0

-- [prod] 机器 root@10.0.1.11:22
  [dry-run] 上传 /tmp/.../imgm-linux-amd64-839f0af5.tar -> /tmp/imgm-linux-amd64-839f0af5.tar
  [dry-run] 远程执行: ctr -n 'k8s.io' images import '/tmp/imgm-linux-amd64-839f0af5.tar'
```

**建议首次向新环境分发前先执行 `--dry-run` 确认。**

## 对远程机器的操作范围

仅有两项操作,`--dry-run` 的输出即为全部内容:

1. **SFTP 上传** tar 至 `remote_tmp`(默认 `/tmp`)
2. **执行一条导入命令** —— `docker load -i <tar>` 或 `ctr -n <ns> images import <tar>`

**不删除任何文件,也不创建目录。** 上传的 tar 会保留在远程机器上,由使用者或系统的 tmp 清理策略处理。`env check` 的全部探测(`docker version` / `ctr --version` / `test -d` / `df` / `uname -m`)均为只读操作,不产生文件。`remote_tmp` 指向的目录必须已存在,不存在时会报错提示更换,而不会自动创建。

唯一会改变远端已有状态的是 `docker load` 本身:导入的 tag 若在远端已存在,将指向新镜像,原镜像变为 dangling。这是镜像导入的固有语义,而非额外操作。

拼接进远程命令的路径与命名空间均已做 shell 引号转义,`remote_tmp` 还额外限制为不含 shell 元字符的绝对路径。

> 上传中断(网络断开、Ctrl-C)时,远端会残留一个不完整的 tar。由于不删除远端文件,imgm 仅在错误信息中提示该文件残缺 —— 重传前需自行删除,否则 `docker load` 会报出难以定位的 tar 解析错误。

## 常用操作

### 查看当前配置

```bash
imgm env ls            # 所有环境
imgm env show prod     # 某个环境的详情
imgm host ls -e prod   # 某个环境的机器
```

```
$ imgm env ls
NAME      TYPE     PLATFORM      HOSTS
prod      k8s      linux/amd64   3
staging   docker   linux/amd64   1
```

### 增减机器

```bash
imgm host add 10.0.1.20 10.0.1.21 -e prod   # 添加, 账号自动继承环境配置
imgm host add 10.0.1.20-23 -e prod          # IP 连续时可用区间简写
imgm host rm 10.0.1.20 -e prod              # 移除
```

区间仅识别最后一段(`10.0.1.11-14` → 11、12、13、14),因此 `node-1`、`web-server-3` 等含连字符的主机名不会被误展开。展开结果会先列出供确认。

添加完成后会输出机器列表与后续的分发命令:

```
$ imgm host add 10.0.1.20 -e prod
✔ 已向环境 prod 添加 1 台机器 (共 4 台)

HOST        PORT   USER   AUTH
10.0.1.11   22     root   password=****(已设置)
10.0.1.12   22     root   password=****(已设置)
10.0.1.13   22     root   password=****(已设置)
10.0.1.20   22     root   password=****(已设置)  ← 新增

下一步:
  imgm env check prod                 # 1. 验证所有机器可连接
  imgm pull -e prod nginx:1.25        # 2. 从 registry 拉取并分发
  imgm build -e prod -t myapp:1.0 .   # 或: 构建本地应用并分发
  imgm push -e prod myapp:1.0         # 或: 分发本机已有镜像
  拉取与构建均按环境架构 linux/amd64 进行, 与本机架构无关。
```

新加入的机器不会自动补齐此前已分发的镜像,需重新执行一次分发命令。

### 变更密码 / 密钥

对整个环境统一变更:

```bash
imgm env set prod --password 'new-pw'
imgm env set prod --key ~/.ssh/deploy_rsa
```

对单台机器单独设置:

```bash
imgm host set 10.0.1.11 -e prod --password 'other-pw'
imgm host set 10.0.1.11 -e prod --port 2222 --user deploy
imgm host set 10.0.1.11 -e prod --password ""    # 空值 = 清除单独设置, 恢复继承环境
```

`env set` 仅影响**未单独设置过账号的机器**,12 台机器更换密钥无需逐台操作。

密钥与密码可同时存在,连接时**优先尝试密钥,失败后回退密码**。因此从密码切换至密钥时无需先清除密码,切换失败仍可连通。

### 同时分发至多个环境

```bash
imgm pull -e prod,staging nginx:1.25
imgm pull --all nginx:1.25
```

架构相同的环境只拉取和打包一次。

### 仅导出 tar 手动迁移

`build` 不带 `-e` 即为纯本地构建,不连接任何远程机器;`-o` 将结果导出为 tar:

```bash
imgm build -t myapp:1.0 --platform linux/amd64 -o myapp.tar .
```

该模式下架构不再由环境决定,缺省为 `linux/amd64`,需要其他架构须显式指定 `--platform`。

```
构建完成。

拷到目标机后手动导入:
  docker load -i myapp.tar                # docker 机器
  ctr -n k8s.io images import myapp.tar   # k8s 节点
```

`-o` 与 `-e` 不能同时使用 —— 分发要求镜像先加载至本机 docker,而 `-o` 直接导出为 tar 不做加载。

不带 `-o` 时镜像加载至本机 docker,输出改为分发提示:

```
构建完成。

下一步:
  imgm push -e <环境> myapp:1.0   # 分发到已配置的环境
  imgm env ls                     # 查看有哪些环境
```

## 两个常见问题

### 问题 1:架构不匹配

在 Mac(Apple Silicon)上开发、向 x86 服务器部署是最常见的故障场景 —— 镜像可以导入,但运行时报 `exec format error`。

**imgm 默认按 `linux/amd64` 处理**(内网服务器绝大多数为 x86),不跟随本机架构。因此在 Mac 上无需额外配置,默认行为即为正确。

在 Mac 上使用默认架构分发时会提示一次,以免目标机实际为 ARM 而未被注意:

```
$ imgm pull -e prod nginx:1.25
ℹ 环境 prod 没设架构, 按默认的 linux/amd64 拉取和打包 (与本机 arm64 无关, 只看目标机)
  目标机若是 ARM: imgm env set prod --platform linux/arm64
```

目标机确为 ARM(鲲鹏、飞腾、树莓派等)时,**在环境上修改一次即可**长期生效:

```bash
imgm env set prod --platform linux/arm64
```

修改后该提示不再出现 —— 显式选择非默认架构即表明已确认过该问题。

需要临时分发其他架构(试机、临时机器)时,`pull` / `push` / `build` 均支持 `--platform` 覆盖本次:

```bash
imgm pull -e prod nginx:1.25 --platform linux/arm64
```

该参数**仅影响本次执行,不修改环境配置**,因此会提示一次以避免误认为配置已变更:

```
ℹ 本次按 --platform linux/arm64 处理, 覆盖环境设置: prod (原 linux/amd64)
  只影响这一次; 要长期改用 imgm env set <环境> --platform linux/arm64
```

其余架构相关保障:

- 环境的 `platform` 决定拉取与打包时的架构,`--platform` 可临时覆盖
- `build -e` 自动按环境架构构建,无需手写 `--platform`
- `push` 上传前校验本机镜像架构,不匹配则拒绝执行
- `env check` 通过 `uname -m` 核对远端真实架构,**架构不一致按失败处理,不降级为警告**

这是本工具的主要设计目标之一。

### 问题 2:k8s 需分发至每个节点

k8s 环境的导入命令 `ctr -n k8s.io images import` 为**单机操作** —— 导入 A 节点后 B 节点仍不存在该镜像。因此环境的 hosts 需列出所有可能被调度到的节点。

| 环境 type | 远端实际执行的导入命令 |
|---|---|
| `docker` | `docker load -i <tar>` |
| `k8s` | `ctr -n k8s.io images import <tar>` |

namespace 必须为 `k8s.io`(默认值),导入至其他 namespace 时 kubelet 无法识别。

> k3s / RKE2 节点不提供独立的 `ctr` 命令(已打包进 k3s 二进制),当前版本会报「未找到 ctr」。

## 经跳板机访问内网机器

内网的常见拓扑:仅有一台机器可从本机直连,其余机器需经它中转。

**imgm 不会自动判定跳板机** —— 它无法区分「该机器需要中转」与「该机器已宕机」,而误判会导致镜像分发至错误的机器,因此需要显式指定。当 `env check` 发现「恰好一台可连接、其余全部不可达」时会给出对应命令:

```
汇总: 3 台, 通过 1, 失败 2

ℹ 当前环境为直连模式: 每台机器都从本机直接连接。
  若其余 2 台只能经 10.0.1.11 中转, 设置跳板机:
      imgm env set prod --jump 10.0.1.11
  设置后重新验证: imgm env check prod
```

执行:

```bash
imgm env set prod --jump 10.0.1.11
```

设置后 imgm 直连 `10.0.1.11`,其余机器全部经它中转。

```
$ imgm env show prod
跳板机:   10.0.1.11 (其余 2 台经它中转)

机器 (3 台):
  HOST        PORT   USER   AUTH
  10.0.1.11   22     root   password=****(已设置)  ← 跳板机
  10.0.1.12   22     root   password=****(已设置)
  10.0.1.13   22     root   password=****(已设置)
```

要点:

- **跳板机自身同样接收镜像。** 它是 hosts 中的一台普通机器,仅额外承担通道职责。
- **无需为跳板机单独配置账号。** 其用户名、端口、密钥、密码取自它自己的 host 记录,该继承环境默认的照常继承。
- **无法确定中转是否需要密码时,密钥与密码可同时配置。** imgm 连接任何机器均为**先尝试密钥,失败后回退密码**。如需事先确认:

  ```bash
  ssh -o BatchMode=yes -o PreferredAuthentications=publickey root@10.0.1.11 true
  #   退出码 0 = 密钥可用, 无需密码
  ssh -J root@10.0.1.11 root@10.0.1.12 uname -m   # 验证整条链路
  ```

- **内网机器的地址在跳板机上解析**,而非本机。因此本机无法解析或 ping 通的内网 IP / 主机名可正常填写。
- 设置跳板机前需先将其加入环境:`imgm host add 10.0.1.11 -e prod`。跳板机被其他机器依赖时不能直接 `host rm`,imgm 会要求先执行 `imgm env set prod --jump ""`。

`env check` 会先单独测试跳板机,不通时直接将其余机器标记为「跳过」,避免等待 N 个必然失败的连接:

```
环境 prod  type=k8s  platform=linux/amd64  jump=10.0.1.11  3 台机器

10.0.1.11:22 (root) [跳板机]
  ✘ SSH 连接        dial tcp 10.0.1.11:22: i/o timeout
10.0.1.12:22 (root)
  ✘ 跳过            跳板机 10.0.1.11 不可用, 无法中转
```

整个环境共用一条跳板连接,不会为每台机器重复向跳板机发起认证(易触发 sshd 的 `MaxStartups` 或 fail2ban)。

跳板机的 sshd 需允许端口转发。未开启时错误信息会直接指明:

```
跳板机 10.0.1.11 不允许端口转发, 无法中转到 10.0.1.12:22:
需要它的 sshd_config 里 AllowTcpForwarding yes
```

## 补充说明

<details>
<summary><b>非交互方式创建环境(脚本场景)</b></summary>

参数完整时不会进入交互:

```bash
# 密钥认证
imgm init -n prod --type docker --platform linux/amd64 \
  --host 10.0.0.1,10.0.0.2 --user root --key ~/.ssh/id_rsa

# 密码认证
imgm init -n prod --type k8s --platform linux/amd64 \
  --host 10.0.1.11-14 --user root --password 'S3cret!' -y

# 带跳板机(--jump 的值必须出现在 --host 中)
imgm init -n prod --type k8s --host 10.0.1.11-14 \
  --user root --password 'S3cret!' --jump 10.0.1.11 -y
```

`--key` 与 `--password` 二选一,提供 `--key` 时不再读取 `--password`。

`--password` 会明文落盘,imgm 写入前要求确认一次,`-y` 可跳过。注意密码会留存于 shell history 中 —— 如有顾虑请省略该参数,由向导以不回显方式询问。

密码含 `!` 时必须使用**单引号**,双引号在 bash 中无法阻止 history expansion。

添加更多环境使用 `imgm env add`,参数与 `init` 一致:

```bash
imgm env add staging --type docker --host 10.0.2.5 --user deploy --key ~/.ssh/id_rsa
```

</details>

<details>
<summary><b>批量指定镜像</b></summary>

镜像列表不保存在配置中 —— 环境是长期状态,镜像是一次性决定。可通过 shell 变量组织:

```bash
IMAGES="nginx:1.25 redis:7.0 mysql:8.0"
imgm pull -e prod $IMAGES

imgm pull -e prod $(grep -v '^#' images.txt)    # 从文件读取, 自行过滤注释
```

</details>

<details>
<summary><b>tar 的存放位置与保留策略</b></summary>

**本机**的 tar 默认存放于系统临时目录,成功后自动删除 —— 此类文件通常达数 GB。远端的副本不删除(参见「对远程机器的操作范围」)。

```bash
imgm pull -e prod nginx:1.25 --keep-tar            # 保留并打印路径
imgm pull -e prod nginx:1.25 --work-dir ./output   # 输出至指定目录(隐含保留)
```

</details>

<details>
<summary><b>配置文件位置与密码存储</b></summary>

配置文件位置、完整结构与取值优先级见[「配置文件」](#配置文件)。

密码可在两个层级设置,**更具体的层级优先**:

```
host add/set --password   (单台机器)
        ↓ 覆盖
env add/set --password    (整个环境)
```

</details>

<details>
<summary><b>关于 -e 参数不可省略</b></summary>

`-e` 为必填参数,imgm 不设「当前环境」概念。

这是有意的设计:「当前环境」意味着执行 `imgm pull nginx` 时需先回忆上次切换到了哪个环境,而分发至错误环境的代价是向生产机器写入了非预期的镜像。

上传前还会再次列出镜像、环境与全部目标机地址供确认,`-y` 可跳过。

</details>

## 命令参考

以下为全部命令的完整说明,与 `imgm <command> --help` 的输出一致。

### 命令总览

| 命令 | 作用 |
|---|---|
| [`imgm init`](#imgm-init) | 首次使用:创建第一个环境(交互向导) |
| [`imgm pull`](#imgm-pull) | 从 registry 拉取镜像并分发到环境所有机器 |
| [`imgm build`](#imgm-build) | 用 buildx 构建镜像,可直接分发到环境 |
| [`imgm push`](#imgm-push) | 把本机已有的镜像分发到环境所有机器 |
| [`imgm env add`](#imgm-env-add) | 新增一个环境 |
| [`imgm env set`](#imgm-env-set) | 修改环境的架构 / 命名空间 / 跳板机 / 默认账号 |
| [`imgm env ls`](#imgm-env-ls) | 列出所有环境 |
| [`imgm env show`](#imgm-env-show) | 查看环境详情 |
| [`imgm env rm`](#imgm-env-rm) | 删除环境 |
| [`imgm env check`](#imgm-env-check) | 逐台验证 SSH / 运行时 / 磁盘 / 架构 |
| [`imgm host add`](#imgm-host-add) | 往环境里添加机器 |
| [`imgm host set`](#imgm-host-set) | 修改机器的账号或地址 |
| [`imgm host ls`](#imgm-host-ls) | 列出环境里的机器 |
| [`imgm host rm`](#imgm-host-rm) | 从环境里移除机器 |
| [`imgm completion`](#imgm-completion) | 生成 shell 补全脚本 |

全局参数:`-h, --help` 查看帮助,`-v, --version` 查看版本。

---

### imgm init

首次使用:创建第一个环境。缺少的参数会以交互方式补问。

```
imgm init [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-n, --name` | string | 环境名。非交互场景必填,否则由向导询问 |
| `--type` | string | 环境类型:`docker` \| `k8s` |
| `--platform` | string | 目标机架构,如 `linux/amd64`(缺省 `linux/amd64`) |
| `--namespace` | string | containerd 命名空间,仅 `k8s` 生效(缺省 `k8s.io`) |
| `--host` | strings | 目标机器,逗号分隔或重复该参数,支持区间 `10.0.0.11-14` |
| `--user` | string | SSH 用户(缺省 `root`) |
| `--port` | int | SSH 端口(缺省 `22`) |
| `--key` | string | 本机 SSH 私钥路径,如 `~/.ssh/id_rsa`。公钥需已在目标机 `authorized_keys` 中 |
| `--password` | string | SSH 密码。**明文存入配置** |
| `--remote-tmp` | string | 远程临时目录(缺省 `/tmp`),必须是已存在的绝对路径 |
| `--jump` | string | 跳板机地址,必须是 `--host` 中的一台(空值表示不用跳板机) |
| `-y, --yes` | bool | 跳过确认(区间展开确认、密码明文告警) |

`--key` 与 `--password` 二选一,同时给出时以 `--key` 为准。

```bash
imgm init                                            # 交互向导
imgm init -n prod --type k8s --host 10.0.1.11-14 \
  --user root --key ~/.ssh/id_rsa                    # 非交互
```

### imgm pull

从 registry 拉取镜像并分发到环境所有机器。按环境的目标架构拉取,打包上传到该环境每台机器并导入。同架构的多个环境只拉取和打包一次。

```
imgm pull -e <env> <image>... [flags]
```

参数见[分发命令通用参数](#分发命令通用参数)。

```bash
imgm pull -e prod nginx:1.25 redis:7.0
imgm pull -e prod,test nginx:1.25
imgm pull --all nginx:1.25 --dry-run
```

### imgm build

用 docker buildx 构建镜像(可跨架构),可直接分发到环境。

```
imgm build -t <image:tag> [-e <env>] [context] [flags]
```

带 `-e`:按该环境的架构构建,并分发到其所有机器。
不带 `-e`:仅在本机构建,不连接任何远程机器;加 `-o` 可导出为 tar 手动迁移。

除[分发命令通用参数](#分发命令通用参数)外,另有:

| 参数 | 类型 | 作用 |
|---|---|---|
| `-t, --tag` | string | 镜像 tag,如 `myapp:1.0`(**必填**) |
| `-f, --file` | string | Dockerfile 路径(缺省 `<context>/Dockerfile`) |
| `--context` | string | 构建上下文目录(缺省 `.`)。也可作为位置参数给出 |
| `-o, --output` | string | 导出 tar 路径。不能与 `-e` 同用 —— 分发要求镜像先加载到本机 docker |

```bash
imgm build -e prod -t myapp:1.0 .
imgm build -e prod -t myapp:1.0 -f deploy/Dockerfile.prod .
imgm build -e prod -t myapp:1.0 --platform linux/arm64 .
imgm build -t myapp:1.0 --platform linux/amd64 -o myapp.tar .
```

### imgm push

把本机 docker 中已有的镜像打包上传并导入。不拉取也不构建,上传前校验镜像存在且架构与环境一致。

```
imgm push -e <env> <image>... [flags]
```

参数见[分发命令通用参数](#分发命令通用参数)。

```bash
imgm push -e prod myapp:1.0
```

### 分发命令通用参数

`pull` / `build` / `push` 共用:

| 参数 | 类型 | 作用 |
|---|---|---|
| `-e, --env` | strings | 目标环境,逗号分隔可多个。与 `--all` 二选一且互斥 |
| `--all` | bool | 分发到所有环境 |
| `--platform` | string | 临时覆盖本次的目标架构(缺省跟随环境)。**不修改环境配置** |
| `--dry-run` | bool | 只打印将要执行的动作,不实际执行 |
| `-y, --yes` | bool | 跳过上传前的确认横幅 |
| `--keep-tar` | bool | 保留本机打好的 tar(缺省成功后删除) |
| `--work-dir` | string | tar 输出目录(缺省临时目录)。指定后隐含 `--keep-tar` |

`build` 不带 `-e` 时上述参数中只有 `--platform` 生效。

### imgm env add

新增一个环境。缺少的参数会以交互方式补问。参数与 [`imgm init`](#imgm-init) 完全一致,区别仅在环境名以位置参数给出(没有 `-n`)。

```
imgm env add <name> [flags]
```

```bash
imgm env add staging
imgm env add staging --type k8s --host 10.0.1.1,10.0.1.2 --user root
```

允许先建不含机器的空环境,之后用 `imgm host add` 补充。

### imgm env set

修改一个已有环境。只改显式给出的字段,其余保持原样。

```
imgm env set <name> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `--type` | string | 环境类型:`docker` \| `k8s` |
| `--platform` | string | 目标机架构,如 `linux/amd64` |
| `--namespace` | string | containerd 命名空间,仅 `k8s` 生效 |
| `--user` | string | 默认 SSH 用户 |
| `--port` | int | 默认 SSH 端口 |
| `--key` | string | 默认私钥路径 |
| `--password` | string | 默认密码。**明文存入配置** |
| `--remote-tmp` | string | 远程临时目录 |
| `--jump` | string | 跳板机地址,必须是本环境的机器之一 |
| `-y, --yes` | bool | 跳过密码明文告警 |

**传空值表示清除该字段**,回到全局默认。改默认账号只影响该环境下没有单独设过账号的机器。

```bash
imgm env set prod --platform linux/arm64
imgm env set prod --key ~/.ssh/deploy_rsa
imgm env set prod --jump 10.0.1.11
imgm env set prod --jump ""      # 取消跳板机
imgm env set prod --user ""      # 清除默认用户
```

改动后会打印「旧 → 新」的差异与当前机器列表;没有任何字段变化时提示「未改动任何字段」。

### imgm env ls

列出所有环境。别名 `list`。无参数。

```
imgm env ls
```

```
NAME      TYPE     PLATFORM      HOSTS
prod      k8s      linux/amd64   3
staging   docker   linux/amd64   1
```

配置有问题的环境会在行尾标注原因。

### imgm env show

查看环境详情:类型、架构、命名空间、远程临时目录、跳板机、默认账号与机器列表。

```
imgm env show <name> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `--reveal` | bool | 显示明文密码(缺省显示为 `****(已设置)`) |

```bash
imgm env show prod
imgm env show prod --reveal
```

### imgm env rm

删除环境。删除前要求确认。

```
imgm env rm <name> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-y, --yes` | bool | 跳过确认 |

```bash
imgm env rm staging
```

### imgm env check

逐台验证 SSH 连接、容器运行时、命名空间、临时目录可写性、剩余磁盘空间与 CPU 架构。全部探测均为只读操作。

```
imgm env check <name> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `--timeout` | duration | 单台机器的连接超时(缺省 `10s`),如 `--timeout 30s` |

任意一台不通即以非零状态码退出,可用于脚本前置检查。环境配了跳板机时会先单独测试跳板机,不通则其余机器直接标记为「跳过」。

```bash
imgm env check prod
imgm env check prod --timeout 30s
```

### imgm host add

往环境里添加一台或多台机器。不指定账号参数时继承环境的默认账号,指定了则只对这几台生效。

```
imgm host add <ip>... -e <env> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-e, --env` | string | 目标环境(**必填**) |
| `--user` | string | 仅这几台的 SSH 用户(缺省继承环境) |
| `--port` | int | 仅这几台的 SSH 端口(缺省继承环境) |
| `--key` | string | 仅这几台的私钥路径(缺省继承环境) |
| `--password` | string | 仅这几台的密码(缺省继承环境) |
| `-y, --yes` | bool | 跳过确认(区间展开确认、密码明文告警) |

地址支持区间简写,仅识别最后一段:`10.0.0.11-14` → 11、12、13、14。

```bash
imgm host add 10.0.0.3 -e prod
imgm host add 10.0.0.10-15 -e prod
imgm host add 10.0.0.5 -e prod --port 2222 --user deploy
```

### imgm host set

修改环境里已有机器的 SSH 账号,或修改机器地址本身。

```
imgm host set <ip>... -e <env> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-e, --env` | string | 目标环境(**必填**) |
| `--host` | string | 新的机器地址。仅单台可用,用于机器搬迁 / 换网段 |
| `--user` | string | SSH 用户。空值表示清除,回到继承环境 |
| `--port` | int | SSH 端口。`0` 表示清除,回到继承环境 |
| `--key` | string | 私钥路径。空值表示清除,回到继承环境 |
| `--password` | string | 密码。空值表示清除,回到继承环境 |
| `-y, --yes` | bool | 跳过确认 |

必须至少指定一个要修改的字段。多台机器中任意一台不存在时整体不生效,不会改到一半。

```bash
imgm host set 10.0.0.3 -e prod --port 2222
imgm host set 10.0.0.3 10.0.0.4 -e prod --user ops
imgm host set 10.0.0.3 -e prod --host 10.0.1.9
imgm host set 10.0.0.3 -e prod --password ""      # 清除单独设置
```

改地址时若该机器是跳板机,环境的 `jump` 会同步更新。

### imgm host ls

列出环境里的机器及其生效的端口、用户、认证方式。别名 `list`。

```
imgm host ls -e <env>
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-e, --env` | string | 目标环境(**必填**) |

### imgm host rm

从环境里移除一台或多台机器。地址同样支持区间简写。

```
imgm host rm <ip>... -e <env> [flags]
```

| 参数 | 类型 | 作用 |
|---|---|---|
| `-e, --env` | string | 目标环境(**必填**) |
| `-y, --yes` | bool | 跳过区间展开确认 |

```bash
imgm host rm 10.0.0.3 -e prod
imgm host rm 10.0.0.11-14 -e prod
```

仍有其他机器依赖该跳板机时不允许移除,需先执行 `imgm env set <env> --jump ""`。

### imgm completion

生成指定 shell 的补全脚本,支持 `bash` / `zsh` / `fish` / `powershell`。

```bash
imgm completion zsh > "${fpath[1]}/_imgm"
imgm completion bash > /etc/bash_completion.d/imgm
```

---

参数不确定时直接执行命令,会输出用法与示例:

```
$ imgm pull
错误: 没有指定要拉取的镜像

用法: imgm pull -e <env> <image>...
示例:
  imgm pull -e prod nginx:1.25 redis:7.0
  imgm pull -e prod,test nginx:1.25
  imgm pull --all nginx:1.25 --dry-run

完整帮助: imgm pull --help
```

## 配置文件

配置位于 `~/.imgm/config.yaml`(文件权限 `0600`、目录 `0700`),由 imgm 读写,无需手动编辑。`IMGM_HOME` 可变更其位置。

完整结构如下,`omitempty` 字段未设置时不会出现在文件中:

```yaml
defaults:                          # 全局默认, 所有环境未设置时回落到这里
  platform: linux/amd64            # 无对应命令, 需要时手动编辑
  remote_tmp: /tmp
  ssh:
    user: root
    port: 22
    key_file: ~/.ssh/id_rsa
    password: ""

environments:
  - name: prod
    type: k8s                      # docker | k8s
    platform: linux/arm64          # 决定拉取 / 构建 / 打包的架构
    containerd_namespace: k8s.io   # 仅 type=k8s 生效
    remote_tmp: /var/tmp           # 上传目标目录, 必须已存在
    jump: 10.0.1.11                # 跳板机, 必须是下方 hosts 中的一台
    ssh:                           # 本环境所有机器共享的默认账号
      user: root
      password: x
    hosts:
      - host: 10.0.1.11            # 未设账号字段则继承上方 ssh
      - host: 10.0.1.12
        port: 2222                 # 仅这台生效, 覆盖环境默认
        user: deploy
```

取值优先级为 **机器 → 环境 → `defaults` → 内置默认**(`linux/amd64` / `/tmp` / `root` / `22` / `k8s.io`)。`environments` 与 `hosts` 由各命令维护,`defaults` 没有对应命令,需要时手动编辑。

`IMGM_HOME` 便于多套配置并存:

```bash
IMGM_HOME=~/.imgm-customer-a imgm env ls
```

**密码以明文存储。** 更安全的方式是使用密钥认证。`env show` / `host ls` 默认将密码显示为 `****(已设置)`,仅 `--reveal` 输出明文。

## 已知限制

- SSH 不校验主机指纹(`InsecureIgnoreHostKey`)—— 内网工具的取舍
- 多台机器为串行传输,单台内部的 SFTP 传输为并发
- 跳板机仅支持一层,且必须是环境内的机器之一(不支持「只转发不部署」的纯通道)
- 隧道上未启用 keepalive,经跳板机执行耗时较长的 `docker load` 可能被防火墙的空闲超时切断 —— **此时导入很可能已经成功**,报错不代表未导入,可在目标机执行 `docker images` 确认
- 旧版本 imgm 会静默忽略配置中的 `jump:` 字段而直连内网机器(配置无版本号)。在多台电脑间拷贝配置时需同步更新二进制
- 上传中断后残留的不完整 tar 需自行清理(imgm 不删除远端文件)
- k3s / RKE2 节点的 `ctr` 路径特殊,当前版本不支持
