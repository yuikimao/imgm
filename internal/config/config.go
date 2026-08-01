package config

import (
	"gopkg.in/yaml.v3"
)

// SSHParams 是环境级 / 全局默认级共享的 SSH 认证参数 (不含具体主机地址)。
type SSHParams struct {
	Port     int    `yaml:"port,omitempty"`
	User     string `yaml:"user,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// Host 表示一台目标机器。空字段继承所属环境与全局默认。
type Host struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port,omitempty"`
	User     string `yaml:"user,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// UnmarshalYAML 容忍手工改过的配置里 hosts 项写成纯字符串 (- 10.0.0.1)。
// CLI 自己写出的始终是对象形式。
func (h *Host) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		h.Host = value.Value
		return nil
	}
	type rawHost Host // 去掉自定义方法, 避免递归
	var r rawHost
	if err := value.Decode(&r); err != nil {
		return err
	}
	*h = Host(r)
	return nil
}

// Defaults 是所有环境共享的默认值, 会被环境级 / 主机级配置覆盖。
type Defaults struct {
	Platform  string    `yaml:"platform,omitempty"`
	RemoteTmp string    `yaml:"remote_tmp,omitempty"`
	SSH       SSHParams `yaml:"ssh,omitempty"`
}

// Environment 表示一个目标环境 (如 prod-docker / prod-k8s), 内含多台机器。
type Environment struct {
	Name                string    `yaml:"name"`
	Type                string    `yaml:"type"` // docker | k8s
	Platform            string    `yaml:"platform,omitempty"`
	ContainerdNamespace string    `yaml:"containerd_namespace,omitempty"` // 仅 type=k8s 生效
	RemoteTmp           string    `yaml:"remote_tmp,omitempty"`
	SSH                 SSHParams `yaml:"ssh,omitempty"` // 该环境所有机器共享的默认 SSH 参数
	Hosts               []Host    `yaml:"hosts,omitempty"`
}

// Config 是 ~/.imgm/config.yaml 的根结构。只由 CLI 读写, 用户无需手改。
type Config struct {
	Defaults     Defaults      `yaml:"defaults,omitempty"`
	Environments []Environment `yaml:"environments,omitempty"`
}

const (
	TypeDocker = "docker"
	TypeK8s    = "k8s"

	DefaultPlatform  = "linux/amd64"
	DefaultNamespace = "k8s.io"
	DefaultRemoteTmp = "/tmp"
	DefaultSSHPort   = 22
	DefaultSSHUser   = "root"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
