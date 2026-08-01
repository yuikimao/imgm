package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"imgm/internal/config"
)

const version = "0.2.0"

// Execute 跑完命令树并返回进程退出码。
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", translateCobraError(err))
		return 1
	}
	return 0
}

// translateCobraError 把 cobra 内置的英文报错换成中文并补上下一步。
// 这些错误在 cobra 内部生成, 拦不住, 只能在出口按文本翻译。
func translateCobraError(err error) error {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, `required flag(s) `):
		// required flag(s) "env" not set
		names := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(msg, "required flag(s) "), " not set"), `"`)
		switch names {
		case "env":
			return fmt.Errorf("缺少 -e <环境>, 没有\"当前环境\"的概念 —— 这是故意的, 防止发错环境\n\n先看有哪些环境: imgm env ls")
		case "tag":
			return fmt.Errorf("缺少 -t <镜像:tag>\n\n示例: imgm build -e prod -t myapp:1.0 .")
		}
		return fmt.Errorf("缺少必填参数 --%s", strings.ReplaceAll(names, `", "`, ", --"))
	case strings.HasPrefix(msg, "unknown command "):
		// unknown command "pul" for "imgm"  (可能跟着 cobra 的 Did you mean 建议)
		name := msg
		if i := strings.Index(msg, `"`); i >= 0 {
			if j := strings.Index(msg[i+1:], `"`); j >= 0 {
				name = msg[i+1 : i+1+j]
			}
		}
		hint := ""
		if sugg := strings.SplitN(msg, "Did you mean this?", 2); len(sugg) == 2 {
			hint = "\n你是想输入:" + strings.TrimRight(sugg[1], "\n")
		}
		return fmt.Errorf("没有 %q 这个命令%s\n\n可用命令: init, env, host, pull, build, push\n看全部: imgm --help", name, hint)
	case strings.HasPrefix(msg, "unknown flag: --"):
		return fmt.Errorf("不认识参数 --%s\n\n看这个命令支持哪些参数: 在命令后加 --help",
			strings.TrimPrefix(msg, "unknown flag: --"))
	case strings.HasPrefix(msg, "unknown shorthand flag: "):
		// unknown shorthand flag: 'Z' in -Z
		f := strings.Trim(strings.SplitN(strings.TrimPrefix(msg, "unknown shorthand flag: "), " in ", 2)[0], "'")
		return fmt.Errorf("不认识参数 -%s\n\n看这个命令支持哪些参数: 在命令后加 --help", f)
	}
	return err
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "imgm",
		Short: "内网镜像迁移工具",
		Long: `imgm — 把镜像从本机搬到内网机器上。

环境 (机器 / 账号 / 架构) 配一次就能一直复用, 配置由 imgm 自己维护在
` + config.Path() + `, 不需要手写 YAML。

典型流程:
  imgm init                          创建第一个环境
  imgm env check prod                确认机器都能连上
  imgm pull -e prod nginx:1.25       拉取并分发到该环境所有机器`,
		SilenceUsage:  true, // 运行期失败不要甩一整页 usage
		SilenceErrors: true, // 错误统一由 Execute 打印
		Version:       version,
	}

	root.AddCommand(
		newInitCmd(),
		newEnvCmd(),
		newHostCmd(),
		newPullCmd(),
		newBuildCmd(),
		newPushCmd(),
	)
	return root
}

// mustLoad 读配置, 并把"文件还不存在"翻译成可操作的提示。
func mustLoad() (*config.Config, error) {
	cfg, err := config.Load()
	if errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("还没有配置 (%s), 先执行: imgm init", config.Path())
	}
	return cfg, err
}

// argsError 组装参数错误: 说清缺什么, 再给一条能照抄的命令。
// cobra 内置校验器只会甩 "requires at least 1 arg(s)", 用户不知道下一步该敲什么。
func argsError(cmd *cobra.Command, what string) error {
	// Use 字段里已经写清了必需参数, UseLine 追加的 [flags] 在这里是噪音。
	usage := strings.TrimSuffix(cmd.UseLine(), " [flags]")
	msg := fmt.Sprintf("%s\n\n用法: %s", what, usage)
	if cmd.Example != "" {
		msg += "\n示例:\n" + strings.TrimRight(cmd.Example, "\n")
	}
	return fmt.Errorf("%s\n\n完整帮助: %s --help", msg, cmd.CommandPath())
}

// wantArgs 要求至少 n 个位置参数, 缺了就报中文错误并附示例。
func wantArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return argsError(cmd, what)
		}
		return nil
	}
}

// wantExactArgs 要求恰好 n 个位置参数。
func wantExactArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return argsError(cmd, what)
		}
		return nil
	}
}

// wantAtMostArgs 允许 0..n 个位置参数, 多给了就报错。
func wantAtMostArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return argsError(cmd, what)
		}
		return nil
	}
}
