package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Target 是某个环境经过默认值继承后的派生视图。所有字段都已填满,
// 可直接用于部署; Config 本身保持磁盘上的原样不受影响。
type Target struct {
	Name      string
	Type      string
	Platform  string
	Namespace string // 仅 type=k8s 有意义
	RemoteTmp string
	Hosts     []Host // Port/User/KeyFile(已展开 ~)/Password 全部填满

	// PlatformIsDefault 表示 Platform 就是内置默认的 linux/amd64。
	// 用于在本机架构与之不符时提示 —— 选了非默认架构就说明已经想过这事了。
	PlatformIsDefault bool
}

var (
	envNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	platformRe  = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)
	namespaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ValidateEnv 校验环境自身字段的合法性 (不含机器)。写入与读取时共用。
func ValidateEnv(e *Environment) error {
	if e.Name == "" {
		return fmt.Errorf("环境名不能为空")
	}
	if !envNameRe.MatchString(e.Name) {
		return fmt.Errorf("环境名 %q 非法: 只能用字母数字开头, 含字母数字和 . _ -", e.Name)
	}
	switch e.Type {
	case TypeDocker, TypeK8s:
	case "":
		return fmt.Errorf("环境 %q 缺少 type (docker|k8s)", e.Name)
	default:
		return fmt.Errorf("环境 %q 的 type 非法: %q (只能是 docker 或 k8s)", e.Name, e.Type)
	}
	if e.Platform != "" {
		if err := ValidatePlatform(e.Platform); err != nil {
			return fmt.Errorf("环境 %q 的 %w", e.Name, err)
		}
	}
	if e.RemoteTmp != "" {
		if err := ValidateRemoteTmp(e.RemoteTmp); err != nil {
			return fmt.Errorf("环境 %q 的 %w", e.Name, err)
		}
	}
	if e.ContainerdNamespace != "" && !namespaceRe.MatchString(e.ContainerdNamespace) {
		return fmt.Errorf("环境 %q 的 containerd_namespace 非法: %q (只能含字母数字和 . _ -)",
			e.Name, e.ContainerdNamespace)
	}
	return nil
}

// ValidatePlatform 校验形如 linux/amd64 的架构串。
func ValidatePlatform(p string) error {
	if !platformRe.MatchString(p) {
		return fmt.Errorf("platform 非法: %q (应形如 linux/amd64)", p)
	}
	return nil
}

// ValidateRemoteTmp 校验远程临时目录。这个值会被拼进远程 shell 命令,
// 拼接处虽然已经做了引号转义, 但这里再挡一层: 只收绝对路径, 且不含
// shell 元字符与换行, 免得某天有人加了新的拼接点却忘了转义。
func ValidateRemoteTmp(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("remote_tmp 必须是绝对路径: %q", p)
	}
	if strings.ContainsAny(p, "'\"`$;&|<>()\n\r\t*?[]{}!#~\\") {
		return fmt.Errorf("remote_tmp 含有不允许的字符: %q (只能是普通路径)", p)
	}
	return nil
}

// ValidateHost 校验一台机器的字段合法性。认证参数可以留空由环境/全局补齐,
// 因此这里只查明确写死的值; 认证是否齐全在 Resolve 时才知道。
func ValidateHost(h *Host) error {
	if h.Host == "" {
		return fmt.Errorf("机器地址不能为空")
	}
	if strings.ContainsAny(h.Host, " \t/") {
		return fmt.Errorf("机器地址 %q 非法", h.Host)
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("机器 %s 的端口 %d 非法 (1-65535)", h.Host, h.Port)
	}
	return nil
}

// Resolve 按 主机 -> 环境 -> 全局默认 -> 内置默认 的顺序生成可部署的 Target。
func (c *Config) Resolve(name string) (*Target, error) {
	e := c.FindEnv(name)
	if e == nil {
		return nil, fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", name)
	}
	if err := ValidateEnv(e); err != nil {
		return nil, err
	}
	if len(e.Hosts) == 0 {
		return nil, fmt.Errorf("环境 %q 还没有机器, 先执行: imgm host add <ip> -e %s", name, name)
	}

	// 向导在非交互时会把默认值直接写进配置, 所以「字段为空」判断不出用户是否
	// 真的想过架构。退一步只看值: 等于内置默认就当作「可能没想过」。
	platform := firstNonEmpty(e.Platform, c.Defaults.Platform, DefaultPlatform)
	t := &Target{
		Name:              e.Name,
		Type:              e.Type,
		Platform:          platform,
		PlatformIsDefault: platform == DefaultPlatform,
		RemoteTmp:         firstNonEmpty(e.RemoteTmp, c.Defaults.RemoteTmp, DefaultRemoteTmp),
		Hosts:             make([]Host, 0, len(e.Hosts)),
	}
	if e.Type == TypeK8s {
		t.Namespace = firstNonEmpty(e.ContainerdNamespace, DefaultNamespace)
	}

	for i := range e.Hosts {
		h, err := c.resolveHost(e, &e.Hosts[i])
		if err != nil {
			return nil, err
		}
		t.Hosts = append(t.Hosts, h)
	}
	return t, nil
}

// ResolveAll 解析多个环境。all 为 true 时忽略 names, 取全部环境。
func (c *Config) ResolveAll(names []string, all bool) ([]*Target, error) {
	if all {
		if len(c.Environments) == 0 {
			return nil, fmt.Errorf("还没有任何环境, 先执行: imgm init")
		}
		names = make([]string, 0, len(c.Environments))
		for _, e := range c.Environments {
			names = append(names, e.Name)
		}
	}

	seen := make(map[string]bool, len(names))
	targets := make([]*Target, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		t, err := c.Resolve(n)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func (c *Config) resolveHost(e *Environment, src *Host) (Host, error) {
	h := *src
	if err := ValidateHost(&h); err != nil {
		return h, fmt.Errorf("环境 %q: %w", e.Name, err)
	}

	h.Port = firstNonZero(h.Port, e.SSH.Port, c.Defaults.SSH.Port, DefaultSSHPort)
	h.User = firstNonEmpty(h.User, e.SSH.User, c.Defaults.SSH.User)
	h.KeyFile = firstNonEmpty(h.KeyFile, e.SSH.KeyFile, c.Defaults.SSH.KeyFile)
	h.Password = firstNonEmpty(h.Password, e.SSH.Password, c.Defaults.SSH.Password)

	if h.User == "" {
		return h, fmt.Errorf("环境 %q 的机器 %s 没有 ssh 用户", e.Name, h.Host)
	}
	if h.KeyFile == "" && h.Password == "" {
		return h, fmt.Errorf("环境 %q 的机器 %s 缺少认证方式 (密钥或密码)", e.Name, h.Host)
	}

	expanded, err := expandHome(h.KeyFile)
	if err != nil {
		return h, fmt.Errorf("环境 %q 的机器 %s 的密钥路径无效: %w", e.Name, h.Host, err)
	}
	h.KeyFile = expanded
	return h, nil
}

// expandHome 把以 ~ 开头的路径展开为绝对路径。只在 Resolve 时调用,
// 绝不写回 Config, 否则配置里的 ~/.ssh/id_rsa 会被永久改成绝对路径。
func expandHome(path string) (string, error) {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
