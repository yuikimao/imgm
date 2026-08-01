package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"imgm/internal/config"
)

// envSpec 承接 init / env add 的 flag。用指针是为了区分"没给这个 flag"
// (交给向导补问) 和"给了空值"。
type envSpec struct {
	name      string
	typ       *string
	platform  *string
	namespace *string
	hosts     []string
	user      *string
	port      *int
	key       *string
	password  *string
	remoteTmp *string
	jump      *string
	yes       bool
}

// registerFields 注册环境自身的字段, init / env add / env set 共用。
func (s *envSpec) registerFields(fs *pflag.FlagSet) {
	s.typ = fs.String("type", "", "环境类型: docker | k8s")
	s.platform = fs.String("platform", "", "目标机架构, 如 linux/amd64 (缺省 linux/amd64)")
	s.namespace = fs.String("namespace", "", "containerd 命名空间, 仅 k8s 生效 (缺省 k8s.io)")
	s.user = fs.String("user", "", "SSH 用户")
	s.port = fs.Int("port", 0, "SSH 端口 (缺省 22)")
	s.key = fs.String("key", "", "本机 SSH 私钥路径, 如 ~/.ssh/id_rsa (公钥需已在目标机 authorized_keys 中)")
	s.password = fs.String("password", "", "SSH 密码 (将明文存入配置)")
	s.remoteTmp = fs.String("remote-tmp", "", "远程临时目录 (缺省 /tmp)")
	s.jump = fs.String("jump", "", "跳板机地址, 必须是本环境的机器之一 (空值表示不用跳板机)")
}

func (s *envSpec) registerYes(fs *pflag.FlagSet) {
	fs.BoolVarP(&s.yes, "yes", "y", false, "跳过确认")
}

func (s *envSpec) register(fs *pflag.FlagSet, withName bool) {
	if withName {
		fs.StringVarP(&s.name, "name", "n", "", "环境名")
	}
	s.registerFields(fs)
	fs.StringSliceVar(&s.hosts, "host", nil, "目标机器, 逗号分隔或重复该 flag, 支持 10.0.0.11-14")
	s.registerYes(fs)
}

// given 判断某个 flag 是否被显式给出 (指针非 nil 且 flag 被 Set)。
func given(fs *pflag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	return f != nil && f.Changed
}

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "管理环境 (一组共享账号与架构的机器)",
	}
	cmd.AddCommand(newEnvAddCmd(), newEnvSetCmd(), newEnvLsCmd(), newEnvShowCmd(), newEnvRmCmd(), newEnvCheckCmd())
	return cmd
}

func newEnvAddCmd() *cobra.Command {
	var spec envSpec
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "新增一个环境",
		Long:  "新增一个环境。缺少的参数会以交互方式补问。",
		Example: `  imgm env add staging
  imgm env add staging --type k8s --host 10.0.1.1,10.0.1.2 --user root`,
		Args: wantExactArgs(1, "没有指定环境名"),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec.name = args[0]
			return upsertEnv(cmd, NewPrompter(), &spec)
		},
	}
	spec.register(cmd.Flags(), false)
	return cmd
}

func newEnvSetCmd() *cobra.Command {
	var spec envSpec
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "修改环境的架构 / 命名空间 / 默认账号",
		Long: `修改一个已有环境。只改显式给出的字段, 其余保持原样。

改默认账号会影响该环境下所有"没有单独设过账号"的机器 —— 换一次密钥比逐台改省事:
  imgm env set prod --key ~/.ssh/deploy_rsa

传空值表示清除该字段, 回到全局默认:
  imgm env set prod --user ""`,
		Example: `  imgm env set prod --platform linux/arm64
  imgm env set prod --key ~/.ssh/deploy_rsa
  imgm env set prod --user ""`,
		Args: wantExactArgs(1, "没有指定要修改的环境名"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			e := cfg.FindEnv(args[0])
			if e == nil {
				return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", args[0])
			}

			fs := cmd.Flags()
			out := cmd.ErrOrStderr()
			stdout := cmd.OutOrStdout()

			// 先改副本再校验, 这样 --type 与 --namespace 谁先谁后都无所谓。
			before := *e
			after := *e
			if given(fs, "type") {
				after.Type = *spec.typ
			}
			if given(fs, "platform") {
				after.Platform = *spec.platform
			}
			if given(fs, "namespace") {
				after.ContainerdNamespace = *spec.namespace
			}
			if given(fs, "remote-tmp") {
				after.RemoteTmp = *spec.remoteTmp
			}
			if given(fs, "jump") {
				after.Jump = *spec.jump
			}
			if given(fs, "user") {
				after.SSH.User = *spec.user
			}
			if given(fs, "port") {
				after.SSH.Port = *spec.port
			}
			if given(fs, "key") {
				after.SSH.KeyFile = *spec.key
			}
			if given(fs, "password") {
				after.SSH.Password = *spec.password
			}

			if envUnchanged(&before, &after) {
				fmt.Fprintln(stdout, "未改动任何字段")
				return nil
			}
			if err := config.ValidateEnv(&after); err != nil {
				return err
			}
			if err := checkJumpMember(after.Jump, hostAddrs(after.Hosts), after.Name); err != nil {
				return err
			}
			if after.Type == config.TypeDocker && after.ContainerdNamespace != "" {
				fmt.Fprintf(out, "⚠ 环境类型是 docker, namespace 暂不生效 (仅 k8s 读取), 已保存备用\n")
			}
			if after.SSH.Password != "" && after.SSH.Password != before.SSH.Password {
				if err := warnPlaintextPassword(out, NewPrompter(), spec.yes); err != nil {
					return err
				}
			}

			*e = after
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "✔ 已更新环境 %s\n\n", e.Name)
			printEnvDiff(stdout, &before, &after)
			if len(e.Hosts) > 0 {
				fmt.Fprintf(stdout, "\n机器 (%d 台):\n", len(e.Hosts))
				printHostTable(stdout, cfg, e, hostTableOpts{Indent: "  "})
			}
			return nil
		},
	}
	spec.registerFields(cmd.Flags())
	spec.registerYes(cmd.Flags())
	return cmd
}

func envUnchanged(a, b *config.Environment) bool {
	return a.Type == b.Type && a.Platform == b.Platform &&
		a.ContainerdNamespace == b.ContainerdNamespace &&
		a.RemoteTmp == b.RemoteTmp && a.Jump == b.Jump && a.SSH == b.SSH
}

// hostAddrs 取出机器地址列表, 供跳板机归属校验用。
func hostAddrs(hosts []config.Host) []string {
	addrs := make([]string, 0, len(hosts))
	for _, h := range hosts {
		addrs = append(addrs, h.Host)
	}
	return addrs
}

// checkJumpMember 校验跳板机确实是本环境的机器之一。空值 (不用跳板机) 直接通过。
// 环境还没有机器时不拦 —— env add 允许先建空骨架, 之后 host add 补机器。
func checkJumpMember(jump string, addrs []string, envName string) error {
	if jump == "" || len(addrs) == 0 {
		return nil
	}
	for _, a := range addrs {
		if a == jump {
			return nil
		}
	}
	return fmt.Errorf("跳板机 %s 不是环境 %q 的机器 (可选: %s)", jump, envName, strings.Join(addrs, ", "))
}

// printEnvDiff 打印 旧 → 新。只列真的变了的字段。密码永不出明文。
func printEnvDiff(out io.Writer, before, after *config.Environment) {
	line := func(label, old, new string) {
		fmt.Fprintf(out, "%s  %s → %s\n", padCJK(label, 8), orDefault(old, "(未设置)"), orDefault(new, "(已清除)"))
	}
	row := func(label, old, new string) {
		if old == new {
			return
		}
		line(label, old, new)
	}
	row("类型", before.Type, after.Type)
	row("架构", before.Platform, after.Platform)
	row("命名空间", before.ContainerdNamespace, after.ContainerdNamespace)
	row("远程临时", before.RemoteTmp, after.RemoteTmp)
	row("跳板机", before.Jump, after.Jump)
	row("默认用户", before.SSH.User, after.SSH.User)
	row("默认端口", portText(before.SSH.Port), portText(after.SSH.Port))
	row("默认私钥", before.SSH.KeyFile, after.SSH.KeyFile)
	// 换成另一个密码时两边都渲染成 ****(已设置), 比对掩码会把这一行整个吞掉。
	if before.SSH.Password != after.SSH.Password {
		if before.SSH.Password != "" && after.SSH.Password != "" {
			line("默认密码", "****(已设置)", "****(已更新)")
		} else {
			line("默认密码", maskPassword(before.SSH.Password), maskPassword(after.SSH.Password))
		}
	}
}

// padCJK 按终端显示宽度右填充。tabwriter 按 rune 计数, 会把中文列算窄一半。
func padCJK(s string, width int) string {
	if w := displayWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// displayWidth 估算终端显示宽度, 中日韩字符按两格算。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2000 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func portText(p int) string {
	if p == 0 {
		return ""
	}
	return strconv.Itoa(p)
}

func maskPassword(s string) string {
	if s == "" {
		return ""
	}
	return "****(已设置)"
}

// askJump 问哪台机器是跳板机。留空表示所有机器都能直连。
func askJump(p *Prompter, hosts []string) (string, error) {
	fmt.Fprintf(p.out, "\n如果这些机器只能经其中一台中转, 输入那一台的地址 (直接回车表示都能直连)\n  可选: %s\n", strings.Join(hosts, ", "))
	for {
		s, err := p.LineOptional("跳板机")
		if err != nil {
			return "", err
		}
		if s == "" {
			return "", nil
		}
		for _, h := range hosts {
			if h == s {
				return s, nil
			}
		}
		fmt.Fprintf(p.out, "  %s 不在上面的机器里, 重新输入或直接回车跳过\n", s)
	}
}

// upsertEnv 是 init 与 env add 的共同实现。
func upsertEnv(cmd *cobra.Command, p *Prompter, spec *envSpec) error {
	cfg, err := config.LoadOrEmpty()
	if err != nil {
		return err
	}
	if cfg.FindEnv(spec.name) != nil {
		return fmt.Errorf("环境 %q 已存在 (imgm env show %s 查看, 或换个名字)", spec.name, spec.name)
	}

	out := cmd.ErrOrStderr()

	fs := cmd.Flags()
	e := config.Environment{Name: spec.name}

	if given(fs, "type") {
		e.Type = *spec.typ
	} else if e.Type, err = p.Choice("环境类型", []string{config.TypeDocker, config.TypeK8s}, config.TypeDocker); err != nil {
		return err
	}

	if given(fs, "platform") {
		e.Platform = *spec.platform
	} else if e.Platform, err = p.ChoiceDefault("目标架构", []string{"linux/amd64", "linux/arm64"}, config.DefaultPlatform); err != nil {
		return err
	}

	if e.Type == config.TypeK8s {
		if given(fs, "namespace") {
			e.ContainerdNamespace = *spec.namespace
		} else if e.ContainerdNamespace, err = p.LineDefault("containerd 命名空间", config.DefaultNamespace); err != nil {
			return err
		}
	}
	if given(fs, "remote-tmp") {
		e.RemoteTmp = *spec.remoteTmp
	}

	hosts := spec.hosts
	if len(hosts) == 0 && p.tty {
		// 向导内部已经展开并回显过, 不需要再展开一遍。
		if hosts, err = p.Hosts("目标机器"); err != nil {
			return err
		}
	} else if len(hosts) > 0 {
		expanded, exps, err := expandHostList(hosts)
		if err != nil {
			return err
		}
		if err := confirmExpansions(out, p, exps, spec.yes); err != nil {
			return err
		}
		hosts = expanded
	}

	// 跳板机必须在机器收集完之后问: e.Hosts 要到函数末尾才填, 这里只有局部的 hosts。
	if given(fs, "jump") {
		e.Jump = *spec.jump
		if err := checkJumpMember(e.Jump, hosts, spec.name); err != nil {
			return err
		}
	} else if len(hosts) > 1 && p.tty {
		if e.Jump, err = askJump(p, hosts); err != nil {
			return err
		}
	}

	// 有机器就必须有账号; 没机器 (env add 只建骨架) 则账号也可以后补。
	if len(hosts) > 0 || given(fs, "user") {
		if given(fs, "user") {
			e.SSH.User = *spec.user
		} else if e.SSH.User, err = p.Line("SSH 用户", config.DefaultSSHUser); err != nil {
			return err
		}

		if given(fs, "port") {
			e.SSH.Port = *spec.port
		} else if p.tty {
			if e.SSH.Port, err = p.Int("SSH 端口", config.DefaultSSHPort); err != nil {
				return err
			}
		}

		switch {
		case given(fs, "key"):
			e.SSH.KeyFile = *spec.key
		case given(fs, "password"):
			e.SSH.Password = *spec.password
		default:
			// 内网机器多数只有密码, 所以直接问密码; 空输入才转密钥。
			if e.SSH.Password, err = p.SecretOptional("SSH 密码 (直接回车改用密钥认证)"); err != nil {
				return err
			}
			if e.SSH.Password == "" {
				// 说清私钥在本机、公钥要事先在目标机上 —— imgm 只读私钥,
				// 不会把公钥装到目标机去 (对远程机器只有查看权限)。
				fmt.Fprintf(out, "\n使用密钥认证。此处填写本机私钥路径, imgm 仅读取该文件。\n")
				fmt.Fprintf(out, "前提: 对应公钥需已配置在目标机的 ~/.ssh/authorized_keys 中。\n")
				if e.SSH.KeyFile, err = p.Line("本机私钥路径", "~/.ssh/id_rsa"); err != nil {
					return err
				}
			}
		}
	}

	for _, h := range hosts {
		e.Hosts = append(e.Hosts, config.Host{Host: h})
	}

	if e.SSH.Password != "" {
		if given(fs, "password") {
			// 密码来自 flag: 可能是脚本里抄来的, 确认一次让用户知道会明文落盘。
			if err := warnPlaintextPassword(out, p, spec.yes); err != nil {
				return err
			}
		} else {
			// 密码是刚在向导里亲手输两遍的, 再问一遍 y/N 只会让人误按回车丢掉整轮输入。
			noticePlaintextPassword(out)
		}
	}

	if err := cfg.AddEnv(e); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	stdout := cmd.OutOrStdout()
	platform := envPlatform(cfg, &e)
	fmt.Fprintf(stdout, "\n✔ 已创建环境 %s (%s, %s, %d 台机器)\n", e.Name, e.Type, platform, len(e.Hosts))
	printNextSteps(stdout, e, platform)
	return nil
}

// cmdSection 打印一组「命令 # 说明」。用 padCJK 而不是 tabwriter:
// tabwriter 按 rune 计数, 含中文的列会算窄一半, 注释就对不齐。
func cmdSection(out io.Writer, title string, rows [][2]string) {
	fmt.Fprintf(out, "\n%s\n", title)
	w := 0
	for _, r := range rows {
		if n := displayWidth(r[0]); n > w {
			w = n
		}
	}
	for _, r := range rows {
		fmt.Fprintf(out, "  %s   # %s\n", padCJK(r[0], w), r[1])
	}
}

// envPlatform 解析环境实际生效的架构: 环境 → 全局默认 → 内置默认。
func envPlatform(cfg *config.Config, e *config.Environment) string {
	return orDefault(e.Platform, orDefault(cfg.Defaults.Platform, config.DefaultPlatform))
}

// printDeploySteps 说明「验证 → 分发」这条主线。host add 之后也要打一次 ——
// 新机器加进来了, 用户接着要问的就是怎么把镜像发过去。
// 架构那句必须带上: 在 Mac 上构建 x86 镜像是这个工具最常被误解的地方,
// 用户会以为 build 跟随本机架构, 于是绕去手写 --platform。
func printDeploySteps(out io.Writer, env, platform string) {
	cmdSection(out, "下一步:", [][2]string{
		{"imgm env check " + env, "1. 验证所有机器可连接"},
		{fmt.Sprintf("imgm pull -e %s nginx:1.25", env), "2. 从 registry 拉取并分发"},
		{fmt.Sprintf("imgm build -e %s -t myapp:1.0 .", env), "或: 构建本地应用并分发"},
		{fmt.Sprintf("imgm push -e %s myapp:1.0", env), "或: 分发本机已有镜像"},
	})
	fmt.Fprintf(out, "  拉取与构建均按环境架构 %s 进行, 与本机架构无关。\n", platform)
}

// printNextSteps 把建完环境后该执行的命令直接拼好。新用户不知道 env check
// 的存在, 更不知道 --jump 和账号继承 —— 让他们去翻 --help 才发现, 不如这里
// 就按「验证 → 分发 → 日常维护」的顺序列出来。
func printNextSteps(out io.Writer, e config.Environment, platform string) {
	if len(e.Hosts) == 0 {
		fmt.Fprintf(out, "\n下一步:\n  imgm host add <ip> -e %s   # 添加机器, 账号继承本环境配置\n", e.Name)
		return
	}

	n := e.Name
	addr := exampleAddr(e.Hosts)
	printDeploySteps(out, n, platform)
	cmdSection(out, "机器管理 (账号默认继承本环境配置):", [][2]string{
		{fmt.Sprintf("imgm host ls -e %s", n), "查看机器列表"},
		{fmt.Sprintf("imgm host add %s -e %s", addr, n), "添加单台"},
		{fmt.Sprintf("imgm host add %s-23 -e %s", addr, n), "批量添加 (20,21,22,23)"},
		{fmt.Sprintf("imgm host rm %s -e %s", addr, n), "移除机器"},
	})
	cmdSection(out, "账号配置 (机器级设置优先于环境级):", [][2]string{
		{fmt.Sprintf("imgm env set %s --password '<密码>'", n), "修改本环境默认账号"},
		{fmt.Sprintf("imgm host add %s -e %s --password '<密码>'", addr, n), "添加时单独指定"},
		{fmt.Sprintf("imgm host set %s -e %s --password '<密码>'", addr, n), "为已有机器单独设置"},
		{fmt.Sprintf("imgm host set %s -e %s --password ''", addr, n), "清除单独设置, 恢复继承"},
	})

	if e.Jump == "" && len(e.Hosts) > 1 {
		fmt.Fprintf(out, "\n当前为直连模式: 每台机器都从本机直接连接。\n")
		fmt.Fprintf(out, "若部分机器只能经其中一台中转, 设置跳板机:\n")
		fmt.Fprintf(out, "  imgm env set %s --jump <跳板机地址>\n", e.Name)
	}
}

// exampleAddr 拿已有机器的网段编一个示例地址, 让 host add 的例子看着像
// 用户自己的网络而不是文档里的 10.0.0.x。主机名或解析不出网段时退回通用例子。
func exampleAddr(hosts []config.Host) string {
	if len(hosts) == 0 {
		return "10.0.0.20"
	}
	m := ipv4Re.FindStringSubmatch(hosts[0].Host)
	if m == nil {
		return "10.0.0.20"
	}
	return m[1] + ".20"
}

// noticePlaintextPassword 只告知, 不拦。
func noticePlaintextPassword(out io.Writer) {
	fmt.Fprintf(out, "\n⚠ 密码以明文保存在 %s (文件权限 0600)。\n", config.Path())
	fmt.Fprintf(out, "  更安全: 用密钥认证 (密码留空即可改填私钥路径)\n")
}

// warnPlaintextPassword 在把密码写进配置前提醒一次。yes 为 true 时静默通过。
func warnPlaintextPassword(out io.Writer, p *Prompter, yes bool) error {
	if yes {
		return nil
	}
	fmt.Fprintf(out, "\n⚠ 密码将以明文保存在 %s (文件权限 0600)。\n", config.Path())
	fmt.Fprintf(out, "  更安全: 用 --key ~/.ssh/id_rsa 走密钥认证\n")
	ok, err := p.Confirm("继续写入?")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("已取消")
	}
	return nil
}

func newEnvLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "列出所有环境",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadOrEmpty()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Environments) == 0 {
				fmt.Fprintln(out, "暂无环境, 执行 imgm init 创建")
				return nil
			}

			tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tPLATFORM\tHOSTS")
			for i := range cfg.Environments {
				e := &cfg.Environments[i]
				platform := envPlatform(cfg, e)
				note := ""
				if err := config.ValidateEnv(e); err != nil {
					note = "  ← 配置有问题: " + err.Error()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d%s\n", e.Name, e.Type, platform, len(e.Hosts), note)
			}
			return tw.Flush()
		},
	}
}

func newEnvShowCmd() *cobra.Command {
	var reveal bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "查看环境详情",
		Example: `  imgm env show prod
  imgm env show prod --reveal`,
		Args: wantExactArgs(1, "没有指定要查看的环境名"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			e := cfg.FindEnv(args[0])
			if e == nil {
				return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", args[0])
			}
			printEnv(cmd.OutOrStdout(), cfg, e, reveal)
			return nil
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "显示明文密码")
	return cmd
}

func printEnv(out io.Writer, cfg *config.Config, e *config.Environment, reveal bool) {
	fmt.Fprintf(out, "环境:     %s\n", e.Name)
	fmt.Fprintf(out, "类型:     %s\n", e.Type)
	fmt.Fprintf(out, "架构:     %s\n", envPlatform(cfg, e))
	if e.Type == config.TypeK8s {
		fmt.Fprintf(out, "命名空间: %s\n", orDefault(e.ContainerdNamespace, config.DefaultNamespace))
	}
	fmt.Fprintf(out, "远程临时: %s\n", orDefault(e.RemoteTmp, orDefault(cfg.Defaults.RemoteTmp, config.DefaultRemoteTmp)))
	if e.Jump != "" {
		if n := len(e.Hosts) - 1; n > 0 {
			fmt.Fprintf(out, "跳板机:   %s (其余 %d 台经它中转)\n", e.Jump, n)
		} else {
			fmt.Fprintf(out, "跳板机:   %s\n", e.Jump)
		}
	}

	fmt.Fprintf(out, "\n默认账号: %s\n", describeAuth(e.SSH, reveal))
	if len(e.Hosts) == 0 {
		fmt.Fprintf(out, "\n机器:     无 (imgm host add <ip> -e %s)\n", e.Name)
		return
	}

	fmt.Fprintf(out, "\n机器 (%d 台):\n", len(e.Hosts))
	printHostTable(out, cfg, e, hostTableOpts{Indent: "  ", Reveal: reveal})
}

// describeAuth 描述认证方式。密码默认打码 —— 终端输出经常被贴进工单。
func describeAuth(s config.SSHParams, reveal bool) string {
	var parts []string
	if s.KeyFile != "" {
		parts = append(parts, "key="+s.KeyFile)
	}
	if s.Password != "" {
		if reveal {
			parts = append(parts, "password="+s.Password)
		} else {
			parts = append(parts, "password="+maskPassword(s.Password))
		}
	}
	if len(parts) == 0 {
		return "未设置"
	}
	return strings.Join(parts, " ")
}

func newEnvRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Short:   "删除环境",
		Example: `  imgm env rm staging`,
		Args:    wantExactArgs(1, "没有指定要删除的环境名"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			e := cfg.FindEnv(args[0])
			if e == nil {
				return fmt.Errorf("不存在名为 %q 的环境", args[0])
			}
			if !yes {
				ok, err := NewPrompter().Confirm(fmt.Sprintf("确认删除环境 %s (%d 台机器)?", e.Name, len(e.Hosts)))
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("已取消")
				}
			}
			if err := cfg.RemoveEnv(args[0]); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✔ 已删除环境 %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认")
	return cmd
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func firstNonZeroInt(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
