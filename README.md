# imgm — 内网镜像迁移工具

内网机器拉不到外网 registry?用 imgm 把镜像从你的电脑搬过去。

它做的事就三步:**本机拉取 → 打包上传 → 远端导入**。机器地址和账号配一次就存下来,以后一条命令搞定。

```bash
imgm pull -e prod nginx:1.25
```

## 安装

```bash
go build -o imgm . && cp imgm "$(go env GOPATH)/bin/"
```

`~/go/bin` 通常已在 PATH 里,不需要 sudo。改完代码重新跑一次这条命令即可 —— 忘了重装的话跑的还是旧版本,可以用 `which -a imgm` 确认只有一份。

- 本机要有 `docker`
- 目标机要有 `docker`,或者 `containerd`(k8s 节点)

## 上手三步

### 第 1 步:告诉 imgm 你的机器在哪

```bash
imgm init
```

它会问你几个问题:

```
环境名 (如 prod / test): prod
环境类型 [docker, k8s] (docker): k8s
目标架构 [linux/amd64, linux/arm64] (linux/amd64):
containerd 命名空间 (k8s.io):
目标机器 (逗号分隔, 支持 10.0.0.11-14, 空行结束): 10.0.1.11-13
SSH 用户 (root):
SSH 端口 (22):
SSH 密码 (直接回车则用密钥认证):
```

括号里是默认值,**直接回车就用默认值**。

关于「环境」:一个环境 = 一组机器 + 一套 SSH 账号 + 一个目标架构。配好之后就用 `-e prod` 引用它。

关于密码:内网机器多数只有密码,所以向导直接问密码(不回显,输两遍)。**想用密钥就在密码这步直接回车**,它会转而问你私钥路径。

### 第 2 步:确认机器连得上

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

**先跑这个**。SSH 密码错了、目标机没装 containerd、磁盘不够,都在这一步暴露,而不是等几 GB 传完才失败。

### 第 3 步:迁镜像

```bash
imgm pull -e prod nginx:1.25 redis:7.0
```

镜像名空格分隔,想写几个写几个。上传前会列出镜像、环境、所有目标机 IP 让你确认一次。

传输过程有进度条,大镜像不用干等:

```
-- [prod] 机器 root@10.0.1.11:22
  上传 /tmp/.../imgm-linux-amd64-d1bbf192.tar -> /tmp/imgm-linux-amd64-d1bbf192.tar (70.5 MiB)
  [===========             ]  47.1%  33.2 MiB / 70.5 MiB  10.4 MiB/s  剩余 3s
  已传 70.5 MiB, 耗时 7s, 平均 10.4 MiB/s
  导入: docker load -i '/tmp/imgm-linux-amd64-d1bbf192.tar'
```

速率是**瞬时值**(滑动平均),链路忙的时候能看出当下有多慢,而不是被开头的快速阶段拉平。慢到几乎不动时剩余时间显示 `--`,不硬算一个没意义的数。输出重定向到文件时会自动改成每 5 秒一行,不会把日志刷成一坨回车符。

**多个镜像会合成一个 tar**,所以上传只发生一次。要迁的镜像尽量凑一批一起发,别一个一个来 —— 几 GB 的传输能省掉好几次。

## 三个动作命令

区别只在**镜像从哪来**,后面的打包 → 上传 → 导入完全一样。

| 命令 | 镜像来源 | 什么时候用 |
|---|---|---|
| `imgm pull` | 从 registry 拉 | 迁第三方镜像,如 nginx、mysql |
| `imgm build` | 本机现场构建 | 迁自己的应用 |
| `imgm push` | 本机 docker 里已有的 | 镜像已经在本地了,不想重新拉 |

```bash
imgm pull  -e prod nginx:1.25 redis:7.0
imgm build -e prod -t myapp:1.0 .
imgm push  -e prod myapp:1.0
```

## 不确定会发生什么?先看一眼

加 `--dry-run`,只打印要执行的命令,一个字节都不传:

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

**养成习惯:第一次发新环境,先 `--dry-run`。**

## 它在远程机器上做什么

只有两件事,`--dry-run` 里看到的就是全部:

1. **SFTP 上传** tar 到 `remote_tmp`(默认 `/tmp`)
2. **执行一条导入命令** —— `docker load -i <tar>` 或 `ctr -n <ns> images import <tar>`

**不删除任何文件,也不创建目录。** 上传的 tar 会留在远程机器上,需要的话由你自己或系统的 tmp 清理策略处理。`env check` 的所有探测(`docker version` / `ctr --version` / `test -d` / `df` / `uname -m`)都是只读的,不落文件。`remote_tmp` 指向的目录必须已经存在,不存在会报错让你换一个,而不是替你创建。

唯一会改变远端已有状态的是 `docker load` 本身:导入的 tag 如果远端已存在,会指向新镜像,旧的变成 dangling。这是镜像导入的固有语义,不是额外动作。

拼进远程命令的路径和命名空间都做了 shell 引号转义,`remote_tmp` 还额外限制为不含 shell 元字符的绝对路径。

> 上传中断(网络断了、Ctrl-C)时,远端会留下一个不完整的 tar。因为不删远端文件,imgm 只能在报错里提示你这个文件残缺 —— 重传前请自己删掉它,否则 `docker load` 会报难懂的 tar 解析错误。

## 常用操作

### 看看现在有什么

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

### 加机器 / 减机器

```bash
imgm host add 10.0.1.20 10.0.1.21 -e prod   # 加,账号自动继承环境
imgm host add 10.0.1.20-23 -e prod          # IP 连续用区间简写
imgm host rm 10.0.1.20 -e prod              # 减
```

区间只认最后一段(`10.0.1.11-14` → 11、12、13、14),所以 `node-1`、`web-server-3` 这类带连字符的主机名不会被误展开。展开结果会先列出来让你确认。

### 换密码 / 换密钥

整个环境一起换:

```bash
imgm env set prod --password 'new-pw'
imgm env set prod --key ~/.ssh/deploy_rsa
```

单台机器特殊对待:

```bash
imgm host set 10.0.1.11 -e prod --password 'other-pw'
imgm host set 10.0.1.11 -e prod --port 2222 --user deploy
imgm host set 10.0.1.11 -e prod --password ""    # 空值 = 撤销特殊设置, 回到跟随环境
```

`env set` 只影响**没有单独设过账号的机器** —— 12 台机器换密钥不用敲 12 次。

密钥和密码可以同时存在,连接时**先试密钥,失败再回退密码**。所以从密码切到密钥时不用先清掉密码,切换失败还能连上。

### 一次发多个环境

```bash
imgm pull -e prod,staging nginx:1.25
imgm pull --all nginx:1.25
```

架构相同的环境只拉取和打包一次。

### 只想要个 tar 自己拷

`build` 不带 `-e` 就是纯本地构建,`-o` 导出 tar:

```bash
imgm build -t myapp:1.0 --platform linux/amd64 -o myapp.tar .
```

拷到目标机后自己导入:

```bash
docker load -i myapp.tar                      # docker 机器
ctr -n k8s.io images import myapp.tar         # k8s 节点
```

## 两个坑

### 坑 1:架构不匹配

Mac(M 系列芯片)上开发、往 x86 服务器部署,是最常见的翻车点 —— 镜像能导进去,但一跑就报 `exec format error`。

**imgm 默认按 `linux/amd64` 走**(绝大多数内网服务器都是 x86),不跟随你本机架构。所以在 Mac 上不用做任何配置,默认就是对的。

在 Mac 上发默认架构时会提示一句,免得目标机其实是 ARM 你却没注意:

```
$ imgm pull -e prod nginx:1.25
ℹ 环境 prod 没设架构, 按默认的 linux/amd64 拉取和打包 (与本机 arm64 无关, 只看目标机)
  目标机若是 ARM: imgm env set prod --platform linux/arm64
```

目标机确实是 ARM(鲲鹏、飞腾、树莓派等)的话,**在环境上改一次就行**,之后一直生效:

```bash
imgm env set prod --platform linux/arm64
```

改完提示就不再出现 —— 选了非默认架构说明你已经想过这事了。

偶尔想临时发一次别的架构(试机、临时机器),`pull` / `push` / `build` 都支持 `--platform` 覆盖本次:

```bash
imgm pull -e prod nginx:1.25 --platform linux/arm64
```

它**只影响这一次,不改环境配置**,所以会提示一句免得你以为已经改掉了:

```
ℹ 本次按 --platform linux/arm64 处理, 覆盖环境设置: prod (原 linux/amd64)
  只影响这一次; 要长期改用 imgm env set <环境> --platform linux/arm64
```

其余架构相关的保障:

- 环境的 `platform` 决定拉取和打包时的架构,`--platform` 可临时覆盖
- `build -e` 自动按环境架构构建,不用手写 `--platform`
- `push` 上传前先校验本机镜像架构,不匹配就拒绝
- `env check` 用 `uname -m` 核对远端真实架构,**架构对不上算失败不算警告**

这也是这个工具存在的主要理由。

### 坑 2:k8s 要发到每个节点

k8s 环境的导入命令是 `ctr -n k8s.io images import`,这是**单机操作** —— 导到 A 节点,B 节点还是没有。所以环境的 hosts 要列出所有可能调度到的节点。

| 环境 type | 远端实际执行的导入命令 |
|---|---|
| `docker` | `docker load -i <tar>` |
| `k8s` | `ctr -n k8s.io images import <tar>` |

namespace 必须是 `k8s.io`(默认值),导到别的 namespace kubelet 看不见。

> k3s / RKE2 节点上没有独立的 `ctr` 命令(被打包进 k3s 二进制了),当前版本会报「未找到 ctr」。

## 细节

<details>
<summary><b>非交互创建环境(写脚本用)</b></summary>

参数给全了就不会进交互:

```bash
# 密钥认证
imgm init -n prod --type docker --platform linux/amd64 \
  --host 10.0.0.1,10.0.0.2 --user root --key ~/.ssh/id_rsa

# 密码认证
imgm init -n prod --type k8s --platform linux/amd64 \
  --host 10.0.1.11-14 --user root --password 'S3cret!' -y
```

`--key` 和 `--password` 二选一,给了 `--key` 就不看 `--password`。

`--password` 会明文落盘,imgm 写入前要你确认一次,`-y` 跳过。注意密码会留在 shell history 里 —— 介意就别带这个 flag,让向导不回显地问你。

密码里有 `!` 一定要用**单引号**,双引号在 bash 里挡不住 history expansion。

再加环境用 `imgm env add`,参数和 `init` 一样:

```bash
imgm env add staging --type docker --host 10.0.2.5 --user deploy --key ~/.ssh/id_rsa
```

</details>

<details>
<summary><b>镜像多了怎么办</b></summary>

镜像列表不存在配置里 —— 环境是长期状态,镜像是一次性决定。用 shell 凑:

```bash
IMAGES="nginx:1.25 redis:7.0 mysql:8.0"
imgm pull -e prod $IMAGES

imgm pull -e prod $(grep -v '^#' images.txt)    # 从文件读, 自己过滤注释
```

</details>

<details>
<summary><b>tar 包放哪 / 怎么留下来</b></summary>

**本机**的 tar 默认放系统临时目录,成功后自动删 —— 这些包动辄好几 GB。远端的那份不删(见上文「它在远程机器上做什么」)。

```bash
imgm pull -e prod nginx:1.25 --keep-tar            # 保留并打印路径
imgm pull -e prod nginx:1.25 --work-dir ./output   # 放指定目录(自动隐含保留)
```

</details>

<details>
<summary><b>配置文件在哪 / 密码怎么存的</b></summary>

`~/.imgm/config.yaml`,文件权限 `0600`、目录 `0700`,由 imgm 自己读写,你不需要手写 YAML。

`IMGM_HOME` 可以换位置,便于多套配置并存:

```bash
IMGM_HOME=~/.imgm-customer-a imgm env ls
```

**密码是明文存的。** 更安全的做法是用密钥认证(向导里密码留空回车,或 `--key ~/.ssh/id_rsa`)。

密码可以在两个层级给,**越具体的赢**:

```
host add/set --password   (单台机器)
        ↓ 覆盖
env add/set --password    (整个环境)
```

`env show` / `host ls` 默认把密码打成 `****(已设置)`,只有 `--reveal` 才出明文。

</details>

<details>
<summary><b>为什么 -e 不能省</b></summary>

`-e` 是必需的,imgm 没有「当前环境」这个概念。

这是故意的:「当前环境」意味着你敲 `imgm pull nginx` 时得先回忆上次切到哪了,而发错环境的代价是往生产机器塞了不该有的镜像。多敲 6 个字符换掉这个风险。

上传前还会再列一次镜像、环境和所有目标机 IP 让你确认,`-y` 跳过。

</details>

<details>
<summary><b>shell 补全</b></summary>

```bash
imgm completion zsh > "${fpath[1]}/_imgm"          # zsh
imgm completion bash > /etc/bash_completion.d/imgm  # bash
```

</details>

## 命令速查

```
imgm init                              创建第一个环境(交互向导)

imgm pull   -e <env> <image>...        从 registry 拉取并分发
imgm build  -e <env> -t <tag> [目录]   构建并分发(不带 -e 则只本地构建)
imgm push   -e <env> <image>...        分发本机已有镜像

imgm env    ls | show | add | set | rm | check
imgm host   ls | add | set | rm
```

`pull` / `build` / `push` 通用参数:

| 参数 | 作用 |
|---|---|
| `-e, --env` | 目标环境,逗号分隔可多个(必需) |
| `--all` | 发到所有环境 |
| `--platform` | 临时覆盖本次的目标架构(缺省跟随环境) |
| `--dry-run` | 只打印要执行什么,不真跑 |
| `-y, --yes` | 跳过确认 |
| `--keep-tar` | 保留本地 tar |
| `--work-dir` | tar 输出目录(隐含 `--keep-tar`) |

忘了参数怎么写就直接敲命令,它会告诉你:

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

## 已知限制

- SSH 不校验主机指纹(`InsecureIgnoreHostKey`)—— 内网工具的取舍
- 多台机器是一台传完再传下一台(单台内部的 SFTP 传输是并发的)
- 不支持跳板机
- 上传中断后残留的不完整 tar 需要你自己清理(imgm 不删远端文件)
- k3s / RKE2 节点上的 `ctr` 路径特殊,当前版本不支持
