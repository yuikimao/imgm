package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"imgm/internal/config"
)

type hostMark int

const (
	markNone hostMark = iota
	markAdded
	markChanged
)

func (m hostMark) label() string {
	switch m {
	case markAdded:
		return "新增"
	case markChanged:
		return "已改"
	default:
		return ""
	}
}

// noteFor 组合一行的尾注, 如 "  ← 跳板机, 新增"。两种标记可以同时出现。
func noteFor(isJump bool, m hostMark) string {
	var parts []string
	if isJump {
		parts = append(parts, "跳板机")
	}
	if s := m.label(); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  ← " + strings.Join(parts, ", ")
}

type hostTableOpts struct {
	Indent string              // env show 里的表格缩进两格
	Reveal bool                // 是否显示明文密码
	Marks  map[string]hostMark // key 是机器地址; nil 表示不标记任何行
}

// printHostTable 打印环境里的机器表格, 每列都按 主机 -> 环境 -> 全局默认 -> 内置默认
// 的顺序解析继承。host ls / host add / host set / env show 共用。
func printHostTable(out io.Writer, cfg *config.Config, e *config.Environment, opts hostTableOpts) {
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintf(tw, "%sHOST\tPORT\tUSER\tAUTH\n", opts.Indent)
	for i := range e.Hosts {
		h := &e.Hosts[i]
		port := firstNonZeroInt(h.Port, e.SSH.Port, cfg.Defaults.SSH.Port, config.DefaultSSHPort)
		user := orDefault(h.User, orDefault(e.SSH.User, cfg.Defaults.SSH.User))
		auth := describeAuth(config.SSHParams{
			KeyFile:  orDefault(h.KeyFile, orDefault(e.SSH.KeyFile, cfg.Defaults.SSH.KeyFile)),
			Password: orDefault(h.Password, orDefault(e.SSH.Password, cfg.Defaults.SSH.Password)),
		}, opts.Reveal)
		fmt.Fprintf(tw, "%s%s\t%d\t%s\t%s%s\n", opts.Indent, h.Host, port, orDefault(user, "-"), auth,
			noteFor(h.Host == e.Jump && e.Jump != "", opts.Marks[h.Host]))
	}
	tw.Flush()
}
