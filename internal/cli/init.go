package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"imgm/internal/config"
)

func newInitCmd() *cobra.Command {
	var spec envSpec
	cmd := &cobra.Command{
		Use:   "init",
		Short: "首次使用: 创建第一个环境",
		Long: `创建第一个环境。缺少的参数会以交互方式补问。

配置写入 ` + config.Path() + `, 之后用 imgm env add 继续添加环境。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := NewPrompter()
			fmt.Fprintf(cmd.ErrOrStderr(), "imgm 初始化 — 配置将写入 %s\n\n", config.Path())

			if spec.name == "" {
				if !p.tty {
					return fmt.Errorf("非交互环境请用 -n 指定环境名")
				}
				name, err := p.Line("环境名 (如 prod / test)", "")
				if err != nil {
					return err
				}
				spec.name = name
			}
			return upsertEnv(cmd, p, &spec)
		},
	}
	spec.register(cmd.Flags(), true)
	return cmd
}
