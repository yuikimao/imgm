package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// errNotTTY 用于在非交互环境下拒绝提问, 而不是阻塞着等 stdin。
type errNotTTY struct{ missing string }

func (e errNotTTY) Error() string {
	return fmt.Sprintf("非交互环境无法提问, 请显式传入 %s", e.missing)
}

// Prompter 是极简交互提示器: bufio 读行 + x/term 做密码遮蔽, 不引 TUI 依赖。
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
	tty bool
}

func NewPrompter() *Prompter {
	return &Prompter{
		in:  bufio.NewReader(os.Stdin),
		out: os.Stderr, // 提示走 stderr, 不污染可能被管道消费的 stdout
		tty: term.IsTerminal(int(os.Stdin.Fd())),
	}
}

// Line 读一行文本。def 非空时回车即取默认值; def 为空表示必填, 空输入会重问。
func (p *Prompter) Line(label, def string) (string, error) {
	if !p.tty {
		return "", errNotTTY{missing: label}
	}
	for {
		if def != "" {
			fmt.Fprintf(p.out, "%s (%s): ", label, def)
		} else {
			fmt.Fprintf(p.out, "%s: ", label)
		}
		s, err := p.in.ReadString('\n')
		if err != nil && s == "" {
			return "", fmt.Errorf("读取输入失败: %w", err)
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s, nil
		}
		if def != "" {
			return def, nil
		}
		fmt.Fprintln(p.out, "  这一项必填")
	}
}

// LineDefault 与 Line 相同, 但非交互环境下直接取默认值而不报错。
// 只用于有安全默认值的项 (架构 / 命名空间), 不能用于必填项。
func (p *Prompter) LineDefault(label, def string) (string, error) {
	if !p.tty {
		return def, nil
	}
	return p.Line(label, def)
}

// Int 读一个正整数。
func (p *Prompter) Int(label string, def int) (int, error) {
	for {
		s, err := p.Line(label, strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			fmt.Fprintln(p.out, "  请输入正整数")
			continue
		}
		return n, nil
	}
}

// Choice 让用户从给定选项里挑一个, 支持唯一前缀匹配。非交互环境报错。
func (p *Prompter) Choice(label string, opts []string, def string) (string, error) {
	for {
		s, err := p.Line(fmt.Sprintf("%s [%s]", label, strings.Join(opts, ", ")), def)
		if err != nil {
			return "", err
		}
		var hit []string
		for _, o := range opts {
			if o == s {
				return o, nil
			}
			if strings.HasPrefix(o, s) {
				hit = append(hit, o)
			}
		}
		if len(hit) == 1 {
			return hit[0], nil
		}
		fmt.Fprintf(p.out, "  只能选: %s\n", strings.Join(opts, ", "))
	}
}

// ChoiceDefault 与 Choice 相同, 但非交互环境下直接取默认值而不报错。
func (p *Prompter) ChoiceDefault(label string, opts []string, def string) (string, error) {
	if !p.tty {
		return def, nil
	}
	return p.Choice(label, opts, def)
}

// SecretOptional 读一个不回显的密码, 并要求确认两遍一致。
// 空输入返回空串而不是重问, 调用方据此转向密钥认证。
func (p *Prompter) SecretOptional(label string) (string, error) {
	if !p.tty {
		return "", errNotTTY{missing: "--password 或 --key"}
	}
	for {
		fmt.Fprintf(p.out, "%s: ", label)
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", fmt.Errorf("读取密码失败: %w", err)
		}
		if len(first) == 0 {
			return "", nil
		}

		fmt.Fprint(p.out, "再次输入: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", fmt.Errorf("读取密码失败: %w", err)
		}
		if string(first) != string(second) {
			fmt.Fprintln(p.out, "  两次输入不一致, 重来")
			continue
		}
		return string(first), nil
	}
}

// Confirm 询问是否继续。非交互环境返回错误, 由调用方提示用户加 -y。
func (p *Prompter) Confirm(label string) (bool, error) {
	if !p.tty {
		return false, fmt.Errorf("非交互环境无法确认, 请加 -y 跳过确认")
	}
	fmt.Fprintf(p.out, "%s [y/N]: ", label)
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		return false, fmt.Errorf("读取输入失败: %w", err)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes", nil
}

// Hosts 读一批机器地址: 一行逗号分隔, 或逐行输入直到空行。支持 10.0.0.11-14 区间简写。
func (p *Prompter) Hosts(label string) ([]string, error) {
	if !p.tty {
		return nil, errNotTTY{missing: "--host"}
	}
	fmt.Fprintf(p.out, "%s (逗号分隔, 支持 10.0.0.11-14, 空行结束): ", label)

	var hosts []string
	for {
		line, err := p.in.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("读取输入失败: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if len(hosts) > 0 {
				return hosts, nil
			}
			fmt.Fprint(p.out, "  至少输入一台机器: ")
			continue
		}
		// 展开失败在交互环境下重问, 比中断整个向导友好。
		expanded, exps, err := expandHostList([]string{line})
		if err != nil {
			fmt.Fprintf(p.out, "\n  %v\n  重新输入: ", err)
			continue
		}
		if len(exps) > 0 {
			fmt.Fprint(p.out, "\n"+describeExpansions(exps))
		}
		hosts = append(hosts, expanded...)
		fmt.Fprintf(p.out, "  已记录 %d 台, 继续输入或直接回车结束: ", len(hosts))
	}
}

// splitList 按逗号切分并去掉空白项。
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
