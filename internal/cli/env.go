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
	yes       bool
}

// registerFields 注册环境自身的字段, init / env add / env set 共用。
func (s *envSpec) registerFields(fs *pflag.FlagSet) {
	s.typ = fs.String("type", "", "环境类型: docker | k8s")
	s.platform = fs.String("platform", "", "目标机架构, 如 linux/amd64 (缺省 linux/amd64)")
	s.namespace = fs.String("namespace", "", "containerd 命名空间, 仅 k8s 生效 (缺省 k8s.io)")
	s.user = fs.String("user", "", "SSH 用户")
	s.port = fs.Int("port", 0, "SSH 端口 (缺省 22)")
	s.key = fs.String("key", "", "SSH 私钥路径, 如 ~/.ssh/id_rsa")
	s.password = fs.String("password", "", "SSH 密码 (将明文存入配置)")
	s.remoteTmp = fs.String("remote-tmp", "", "远程临时目录 (缺省 /tmp)")
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
		a.RemoteTmp == b.RemoteTmp && a.SSH == b.SSH
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
	w := 0
	for _, r := range s {
		if r > 0x2000 {
			w += 2
		} else {
			w++
		}
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
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
			if e.SSH.Password, err = p.SecretOptional("SSH 密码 (直接回车则用密钥认证)"); err != nil {
				return err
			}
			if e.SSH.Password == "" {
				if e.SSH.KeyFile, err = p.Line("私钥路径", "~/.ssh/id_rsa"); err != nil {
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
	fmt.Fprintf(stdout, "\n✔ 已创建环境 %s (%s, %s, %d 台机器)\n", e.Name, e.Type, orDefault(e.Platform, config.DefaultPlatform), len(e.Hosts))
	if len(e.Hosts) == 0 {
		fmt.Fprintf(stdout, "下一步: imgm host add <ip> -e %s\n", e.Name)
	} else {
		fmt.Fprintf(stdout, "下一步: imgm env check %s\n", e.Name)
	}
	return nil
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
				platform := orDefault(e.Platform, orDefault(cfg.Defaults.Platform, config.DefaultPlatform))
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
	fmt.Fprintf(out, "架构:     %s\n", orDefault(e.Platform, orDefault(cfg.Defaults.Platform, config.DefaultPlatform)))
	if e.Type == config.TypeK8s {
		fmt.Fprintf(out, "命名空间: %s\n", orDefault(e.ContainerdNamespace, config.DefaultNamespace))
	}
	fmt.Fprintf(out, "远程临时: %s\n", orDefault(e.RemoteTmp, orDefault(cfg.Defaults.RemoteTmp, config.DefaultRemoteTmp)))

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
