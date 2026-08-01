package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"imgm/internal/config"
)

func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "管理环境里的机器",
	}
	cmd.AddCommand(newHostAddCmd(), newHostSetCmd(), newHostLsCmd(), newHostRmCmd())
	return cmd
}

// confirmExpansions 在区间被展开时让用户确认。没有展开就什么都不做。
func confirmExpansions(out io.Writer, p *Prompter, exps []rangeExpansion, yes bool) error {
	if len(exps) == 0 || yes {
		return nil
	}
	fmt.Fprint(out, describeExpansions(exps))
	ok, err := p.Confirm("继续?")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("已取消")
	}
	return nil
}

func newHostAddCmd() *cobra.Command {
	var (
		envName  string
		user     string
		port     int
		key      string
		password string
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "add <ip>... -e <env>",
		Short: "往环境里添加机器",
		Long: `往环境里添加一台或多台机器。

不指定账号参数时继承环境的默认账号; 指定了则只对这几台生效。
地址支持区间简写: imgm host add 10.0.0.11-14 -e prod`,
		Example: `  imgm host add 10.0.0.3 -e prod
  imgm host add 10.0.0.10-15 -e prod
  imgm host add 10.0.0.5 -e prod --port 2222 --user deploy`,
		Args: wantArgs(1, "没有指定要添加的机器地址"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			addrs, exps, err := expandHostList(args)
			if err != nil {
				return err
			}

			out := cmd.ErrOrStderr()
			p := NewPrompter()
			if err := confirmExpansions(out, p, exps, yes); err != nil {
				return err
			}
			if password != "" {
				if err := warnPlaintextPassword(out, p, yes); err != nil {
					return err
				}
			}

			marks := make(map[string]hostMark, len(addrs))
			for _, addr := range addrs {
				h := config.Host{Host: addr, Port: port, User: user, KeyFile: key, Password: password}
				if err := cfg.AddHost(envName, h); err != nil {
					return err
				}
				marks[addr] = markAdded
			}
			if err := config.Save(cfg); err != nil {
				return err
			}

			e := cfg.FindEnv(envName)
			stdout := cmd.OutOrStdout()
			fmt.Fprintf(stdout, "✔ 已向环境 %s 添加 %d 台机器 (共 %d 台)\n\n", envName, len(addrs), len(e.Hosts))
			printHostTable(stdout, cfg, e, hostTableOpts{Marks: marks})
			fmt.Fprintf(stdout, "\n下一步: imgm env check %s\n", envName)
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&envName, "env", "e", "", "目标环境 (必填)")
	fs.StringVar(&user, "user", "", "仅这几台的 SSH 用户 (缺省继承环境)")
	fs.IntVar(&port, "port", 0, "仅这几台的 SSH 端口 (缺省继承环境)")
	fs.StringVar(&key, "key", "", "仅这几台的私钥路径 (缺省继承环境)")
	fs.StringVar(&password, "password", "", "仅这几台的密码 (缺省继承环境)")
	fs.BoolVarP(&yes, "yes", "y", false, "跳过确认")
	cmd.MarkFlagRequired("env")
	return cmd
}

func newHostSetCmd() *cobra.Command {
	var (
		envName  string
		newHost  string
		user     string
		port     int
		key      string
		password string
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "set <ip>... -e <env>",
		Short: "修改环境里已有机器的账号或地址",
		Long: `修改环境里已有机器的 SSH 账号, 或改掉机器地址本身。

传空值表示清除这台机器的个例设置, 回到继承环境默认:
  imgm host set 10.0.0.3 -e prod --user ""     # USER 回到环境默认
  imgm host set 10.0.0.3 -e prod --port 0      # PORT 回到环境默认

--host 用于机器换了地址 (搬迁 / 换网段), 只能对单台使用:
  imgm host set 10.0.0.3 -e prod --host 10.0.1.9`,
		Example: `  imgm host set 10.0.0.3 -e prod --port 2222
  imgm host set 10.0.0.3 10.0.0.4 -e prod --user ops
  imgm host set 10.0.0.3 -e prod --host 10.0.1.9`,
		Args: wantArgs(1, "没有指定要修改的机器地址"),
		RunE: func(cmd *cobra.Command, args []string) error {
			fs := cmd.Flags()
			if given(fs, "host") && len(args) != 1 {
				return fmt.Errorf("--host 只能用于单台机器改地址 (当前给了 %d 台)", len(args))
			}

			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			addrs, exps, err := expandHostList(args)
			if err != nil {
				return err
			}

			out := cmd.ErrOrStderr()
			p := NewPrompter()
			if err := confirmExpansions(out, p, exps, yes); err != nil {
				return err
			}
			if given(fs, "password") && password != "" {
				if err := warnPlaintextPassword(out, p, yes); err != nil {
					return err
				}
			}

			e := cfg.FindEnv(envName)
			if e == nil {
				return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", envName)
			}

			// 先确认每台都在, 再动手 —— 免得改了一半发现第三台不存在。
			for _, addr := range addrs {
				if e.FindHost(addr) == nil {
					return fmt.Errorf("环境 %s 里没有机器 %s (imgm host ls -e %s 查看)", envName, addr, envName)
				}
			}
			if !given(fs, "host") && !given(fs, "user") && !given(fs, "port") &&
				!given(fs, "key") && !given(fs, "password") {
				return fmt.Errorf("没有指定要改什么 (--host / --user / --port / --key / --password)")
			}

			marks := make(map[string]hostMark, len(addrs))
			for _, addr := range addrs {
				h := e.FindHost(addr)
				if given(fs, "user") {
					h.User = user
				}
				if given(fs, "port") {
					h.Port = port
				}
				if given(fs, "key") {
					h.KeyFile = key
				}
				if given(fs, "password") {
					h.Password = password
				}
				if err := config.ValidateHost(h); err != nil {
					return err
				}
				marks[addr] = markChanged
			}
			if given(fs, "host") {
				if err := cfg.RenameHost(envName, addrs[0], newHost); err != nil {
					return err
				}
				delete(marks, addrs[0])
				marks[newHost] = markChanged
			}

			if err := config.Save(cfg); err != nil {
				return err
			}
			stdout := cmd.OutOrStdout()
			fmt.Fprintf(stdout, "✔ 已更新环境 %s 的 %d 台机器\n\n", envName, len(addrs))
			printHostTable(stdout, cfg, e, hostTableOpts{Marks: marks})
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&envName, "env", "e", "", "目标环境 (必填)")
	fs.StringVar(&newHost, "host", "", "新的机器地址 (仅单台可用)")
	fs.StringVar(&user, "user", "", "SSH 用户 (空值表示清除, 回到继承环境)")
	fs.IntVar(&port, "port", 0, "SSH 端口 (0 表示清除, 回到继承环境)")
	fs.StringVar(&key, "key", "", "私钥路径 (空值表示清除, 回到继承环境)")
	fs.StringVar(&password, "password", "", "密码 (空值表示清除, 回到继承环境)")
	fs.BoolVarP(&yes, "yes", "y", false, "跳过确认")
	cmd.MarkFlagRequired("env")
	return cmd
}

func newHostLsCmd() *cobra.Command {
	var envName string
	cmd := &cobra.Command{
		Use:     "ls -e <env>",
		Aliases: []string{"list"},
		Short:   "列出环境里的机器",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			e := cfg.FindEnv(envName)
			if e == nil {
				return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", envName)
			}
			out := cmd.OutOrStdout()
			if len(e.Hosts) == 0 {
				fmt.Fprintf(out, "环境 %s 还没有机器, 执行: imgm host add <ip> -e %s\n", envName, envName)
				return nil
			}
			printHostTable(out, cfg, e, hostTableOpts{})
			return nil
		},
	}
	cmd.Flags().StringVarP(&envName, "env", "e", "", "目标环境 (必填)")
	cmd.MarkFlagRequired("env")
	return cmd
}

func newHostRmCmd() *cobra.Command {
	var (
		envName string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "rm <ip>... -e <env>",
		Short: "从环境里移除机器",
		Long: `从环境里移除一台或多台机器。

地址支持区间简写: imgm host rm 10.0.0.11-14 -e prod`,
		Example: `  imgm host rm 10.0.0.3 -e prod
  imgm host rm 10.0.0.11-14 -e prod`,
		Args: wantArgs(1, "没有指定要移除的机器地址"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			addrs, exps, err := expandHostList(args)
			if err != nil {
				return err
			}
			if err := confirmExpansions(cmd.ErrOrStderr(), NewPrompter(), exps, yes); err != nil {
				return err
			}
			for _, addr := range addrs {
				if err := cfg.RemoveHost(envName, addr); err != nil {
					return err
				}
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			e := cfg.FindEnv(envName)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✔ 已从环境 %s 移除 %d 台机器 (剩 %d 台)\n", envName, len(addrs), len(e.Hosts))
			if len(e.Hosts) == 0 {
				fmt.Fprintf(out, "⚠ 环境 %s 现在没有任何机器, 无法部署\n", envName)
				return nil
			}
			fmt.Fprintln(out)
			printHostTable(out, cfg, e, hostTableOpts{})
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&envName, "env", "e", "", "目标环境 (必填)")
	fs.BoolVarP(&yes, "yes", "y", false, "跳过确认")
	cmd.MarkFlagRequired("env")
	return cmd
}
