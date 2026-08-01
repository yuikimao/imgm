package cli

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"imgm/internal/config"
	"imgm/internal/remote"
)

// 探测结果等级。
const (
	statusOK = iota
	statusWarn
	statusFail
)

type probe struct {
	status int
	label  string
	detail string
}

func (p probe) mark() string {
	switch p.status {
	case statusFail:
		return "✘"
	case statusWarn:
		return "⚠"
	default:
		return "✔"
	}
}

// uname -m 到 platform 的映射, 用于判断远端架构是否与环境声明一致。
var archAliases = map[string]string{
	"x86_64":  "linux/amd64",
	"amd64":   "linux/amd64",
	"aarch64": "linux/arm64",
	"arm64":   "linux/arm64",
}

const lowDiskGiB = 5.0

func newEnvCheckCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "check <name>",
		Short:   "自检环境: 逐台验证 SSH / 运行时 / 磁盘 / 架构",
		Example: `  imgm env check prod`,
		Args:    wantExactArgs(1, "没有指定要自检的环境名"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mustLoad()
			if err != nil {
				return err
			}
			t, err := cfg.Resolve(args[0])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "环境 %s  type=%s  platform=%s", t.Name, t.Type, t.Platform)
			if t.Type == config.TypeK8s {
				fmt.Fprintf(out, "  ns=%s", t.Namespace)
			}
			fmt.Fprintf(out, "  %d 台机器\n", len(t.Hosts))

			results := make([][]probe, len(t.Hosts))
			var wg sync.WaitGroup
			for i, h := range t.Hosts {
				wg.Add(1)
				go func(i int, h config.Host) {
					defer wg.Done()
					results[i] = checkHost(t, h, timeout)
				}(i, h)
			}
			wg.Wait()

			ok := 0
			for i, h := range t.Hosts {
				fmt.Fprintf(out, "\n%s:%d (%s)\n", h.Host, h.Port, h.User)
				failed := false
				for _, p := range results[i] {
					fmt.Fprintf(out, "  %s %-14s %s\n", p.mark(), p.label, p.detail)
					if p.status == statusFail {
						failed = true
					}
				}
				if !failed {
					ok++
				}
			}

			fmt.Fprintf(out, "\n汇总: %d 台, 通过 %d, 失败 %d\n", len(t.Hosts), ok, len(t.Hosts)-ok)
			if ok < len(t.Hosts) {
				return fmt.Errorf("环境 %s 有 %d 台机器未通过自检", t.Name, len(t.Hosts)-ok)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "单台机器的连接超时")
	return cmd
}

func checkHost(t *config.Target, h config.Host, timeout time.Duration) []probe {
	start := time.Now()
	client, err := remote.DialTimeout(h, timeout)
	if err != nil {
		return []probe{{statusFail, "SSH 连接", err.Error()}}
	}
	defer client.Close()

	probes := []probe{{statusOK, "SSH 连接", time.Since(start).Round(time.Millisecond).String()}}
	probes = append(probes, checkRuntime(client, t))
	if t.Type == config.TypeK8s {
		probes = append(probes, checkNamespace(client, t))
	}
	probes = append(probes, checkTmpDir(client, t), checkDisk(client, t), checkArch(client, t))
	return probes
}

func checkRuntime(c *remote.Client, t *config.Target) probe {
	if t.Type == config.TypeK8s {
		out, err := c.Output("ctr --version")
		if err != nil {
			return probe{statusFail, "运行时 ctr", "未找到 ctr (containerd 未安装或不在 PATH)"}
		}
		return probe{statusOK, "运行时 ctr", lastField(out)}
	}
	out, err := c.Output("docker version --format '{{.Server.Version}}'")
	if err != nil {
		return probe{statusFail, "运行时 docker", "docker 不可用: " + firstLine(out)}
	}
	return probe{statusOK, "运行时 docker", out}
}

func checkNamespace(c *remote.Client, t *config.Target) probe {
	label := "命名空间"
	if _, err := c.Output(fmt.Sprintf("ctr -n %s images ls -q", remote.ShellQuote(t.Namespace))); err != nil {
		return probe{statusFail, label, fmt.Sprintf("%s 不可访问 (权限不足或 containerd 未运行)", t.Namespace)}
	}
	return probe{statusOK, label, t.Namespace + " 可访问"}
}

// checkTmpDir 只读地判断目录是否存在且可写。不落任何探测文件 ——
// 本工具不在别人的机器上创建或删除文件, 除了显式上传的 tar。
func checkTmpDir(c *remote.Client, t *config.Target) probe {
	dir := remote.ShellQuote(t.RemoteTmp)
	cmd := fmt.Sprintf("test -d %s && test -w %s", dir, dir)
	if _, err := c.Output(cmd); err != nil {
		return probe{statusFail, "临时目录", fmt.Sprintf("%s 不存在或不可写", t.RemoteTmp)}
	}
	return probe{statusOK, "临时目录", t.RemoteTmp + " 可写"}
}

func checkDisk(c *remote.Client, t *config.Target) probe {
	out, err := c.Output(fmt.Sprintf("df -Pk %s | tail -1 | awk '{print $4}'", remote.ShellQuote(t.RemoteTmp)))
	if err != nil {
		return probe{statusWarn, "剩余空间", "无法获取"}
	}
	kb, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return probe{statusWarn, "剩余空间", "无法解析: " + out}
	}
	gib := kb / (1024 * 1024)
	detail := fmt.Sprintf("%.1f GiB", gib)
	if gib < lowDiskGiB {
		return probe{statusWarn, "剩余空间", detail + " (偏小, 大镜像可能装不下)"}
	}
	return probe{statusOK, "剩余空间", detail}
}

// checkArch 是本工具存在的全部理由 —— 架构不匹配算失败, 不是警告。
func checkArch(c *remote.Client, t *config.Target) probe {
	out, err := c.Output("uname -m")
	if err != nil {
		return probe{statusWarn, "架构", "无法获取"}
	}
	got, known := archAliases[out]
	if !known {
		return probe{statusWarn, "架构", fmt.Sprintf("%s (无法与 %s 比对)", out, t.Platform)}
	}
	if got != t.Platform {
		return probe{statusFail, "架构", fmt.Sprintf("远端是 %s (%s), 环境声明 %s — 导入的镜像跑不起来", out, got, t.Platform)}
	}
	return probe{statusOK, "架构", fmt.Sprintf("%s → %s", out, got)}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastField(s string) string {
	fields := strings.Fields(firstLine(s))
	if len(fields) == 0 {
		return s
	}
	return fields[len(fields)-1]
}
