package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrNotFound 表示配置文件还不存在, 调用方应提示用户先执行 imgm init。
var ErrNotFound = errors.New("未找到配置文件")

// Dir 返回配置目录。IMGM_HOME 可覆盖, 便于测试与多份配置并存。
func Dir() string {
	if d := os.Getenv("IMGM_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".imgm"
	}
	return filepath.Join(home, ".imgm")
}

// Path 返回配置文件完整路径。
func Path() string {
	return filepath.Join(Dir(), "config.yaml")
}

// Load 读取配置。文件不存在时返回 ErrNotFound。
// 不做任何字段归一化或路径展开 —— 派生值一律走 Resolve, 否则 load-then-save
// 会把 defaults 实体化进每个环境、把 ~ 展开成绝对路径, 永久破坏继承关系。
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取配置 %s 失败: %w", Path(), err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", Path(), err)
	}

	seen := make(map[string]bool, len(cfg.Environments))
	for _, e := range cfg.Environments {
		if seen[e.Name] {
			return nil, fmt.Errorf("配置 %s 已损坏: 环境名重复: %s", Path(), e.Name)
		}
		seen[e.Name] = true
	}
	return &cfg, nil
}

// LoadOrEmpty 与 Load 相同, 但文件不存在时返回空配置。供 init / env add 使用。
func LoadOrEmpty() (*Config, error) {
	cfg, err := Load()
	if errors.Is(err, ErrNotFound) {
		return &Config{}, nil
	}
	return cfg, err
}

// Save 原子写入配置: 同目录建临时文件 -> 0600 -> fsync -> rename。
// 临时文件必须与目标同目录, 否则 rename 跨文件系统就不是原子操作。
func Save(c *Config) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config.yaml.*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后这里是 no-op

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置文件权限失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, Path()); err != nil {
		return fmt.Errorf("写入配置 %s 失败: %w", Path(), err)
	}
	return nil
}
