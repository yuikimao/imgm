package migrate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"imgm/internal/config"
	"imgm/internal/dockercli"
	"imgm/internal/remote"
)

// Obtainer 负责让镜像出现在本机 docker 里 (拉取 / 构建 / 仅校验已存在)。
// 后续的打包-上传-导入对三种来源完全一致。
type Obtainer struct {
	// Do 实际执行准备动作。
	Do func(t *config.Target, images []string) error
	// Plan 返回 --dry-run 下要展示的 docker 命令。
	Plan func(t *config.Target, images []string) [][]string
}

// Options 描述一次部署。
type Options struct {
	Images  []string
	Obtain  Obtainer
	WorkDir string // 空则用临时目录, 且成功后自动删除
	KeepTar bool
	DryRun  bool
	Out     io.Writer
}

// Report 汇总部署结果。
type Report struct {
	HostTotal int
	HostOK    int
	Failures  []string
}

// Deploy 把镜像分发到多个环境的所有机器。
// 同架构的多个环境复用同一份 tar, 只准备和打包一次。
func Deploy(targets []*config.Target, opts Options) (*Report, error) {
	if len(opts.Images) == 0 {
		return nil, fmt.Errorf("没有指定任何镜像")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	workDir, cleanupDir, err := prepareWorkDir(opts)
	if err != nil {
		return nil, err
	}
	if cleanupDir != nil {
		defer cleanupDir()
	}

	rep := &Report{}
	tars := make(map[string]string, len(targets)) // platform -> 本地 tar

	for _, t := range targets {
		fmt.Fprintf(out, "\n#### 环境 [%s] type=%s platform=%s 机器数=%d ####\n",
			t.Name, t.Type, t.Platform, len(t.Hosts))

		tar, ok := tars[t.Platform]
		if !ok {
			tar = filepath.Join(workDir, tarName(t.Platform))
			if err := prepareTar(out, t, tar, opts); err != nil {
				fmt.Fprintf(os.Stderr, "!! 环境 [%s] 准备阶段失败, 跳过其所有机器: %v\n", t.Name, err)
				for _, h := range t.Hosts {
					rep.HostTotal++
					rep.Failures = append(rep.Failures, t.Name+"/"+h.Host)
				}
				continue
			}
			tars[t.Platform] = tar
		} else {
			fmt.Fprintf(out, "-- 复用已打好的 %s (同架构)\n", filepath.Base(tar))
		}

		remoteTar := path.Join(t.RemoteTmp, filepath.Base(tar))
		importCmd := importCommand(t, remoteTar)

		for _, h := range t.Hosts {
			rep.HostTotal++
			fmt.Fprintf(out, "\n-- [%s] 机器 %s@%s:%d\n", t.Name, h.User, h.Host, h.Port)
			if opts.DryRun {
				fmt.Fprintf(out, "  [dry-run] 上传 %s -> %s\n", tar, remoteTar)
				fmt.Fprintf(out, "  [dry-run] 远程执行: %s\n", importCmd)
				rep.HostOK++
				continue
			}
			if err := deployToHost(out, t, h, tar, remoteTar, importCmd); err != nil {
				fmt.Fprintf(os.Stderr, "!! [%s/%s] 失败: %v\n", t.Name, h.Host, err)
				rep.Failures = append(rep.Failures, t.Name+"/"+h.Host)
				continue
			}
			rep.HostOK++
			fmt.Fprintf(out, "== [%s/%s] 完成 ==\n", t.Name, h.Host)
		}
	}

	fmt.Fprintf(out, "\n#### 汇总: 机器总数 %d, 成功 %d, 失败 %d ####\n",
		rep.HostTotal, rep.HostOK, len(rep.Failures))
	if opts.KeepTar && !opts.DryRun {
		for _, tar := range tars {
			if abs, err := filepath.Abs(tar); err == nil {
				fmt.Fprintf(out, "tar 已保留: %s\n", abs)
			}
		}
	}
	if len(rep.Failures) > 0 {
		return rep, fmt.Errorf("以下目标失败:\n  - %s", strings.Join(rep.Failures, "\n  - "))
	}
	return rep, nil
}

// prepareWorkDir 决定 tar 落盘位置。用户指定了目录就不动它 (也不删里面的文件);
// 否则建临时目录, 并在未要求保留时返回清理函数。
func prepareWorkDir(opts Options) (string, func(), error) {
	if opts.WorkDir != "" {
		if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
			return "", nil, fmt.Errorf("创建工作目录 %s 失败: %w", opts.WorkDir, err)
		}
		return opts.WorkDir, nil, nil
	}
	dir, err := os.MkdirTemp("", "imgm-")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	if opts.KeepTar || opts.DryRun {
		return dir, nil, nil
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// prepareTar 在本机准备好镜像并打成一个 tar。
func prepareTar(out io.Writer, t *config.Target, tar string, opts Options) error {
	if opts.DryRun {
		if opts.Obtain.Plan != nil {
			for _, args := range opts.Obtain.Plan(t, opts.Images) {
				fmt.Fprintf(out, "  [dry-run] docker %s\n", strings.Join(args, " "))
			}
		}
		fmt.Fprintf(out, "  [dry-run] docker %s\n",
			strings.Join(dockercli.SaveArgs(opts.Images, tar, t.Platform), " "))
		return nil
	}

	if opts.Obtain.Do != nil {
		if err := opts.Obtain.Do(t, opts.Images); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "-- 打包 %d 个镜像 -> %s\n", len(opts.Images), tar)
	return dockercli.Save(opts.Images, tar, t.Platform)
}

// PullObtainer 从 registry 按目标架构拉取镜像。
func PullObtainer() Obtainer {
	return Obtainer{
		Do: func(t *config.Target, images []string) error {
			for _, img := range images {
				fmt.Printf("-- 拉取镜像 %s (%s)\n", img, t.Platform)
				if err := dockercli.Pull(img, t.Platform); err != nil {
					return err
				}
			}
			return nil
		},
		Plan: func(t *config.Target, images []string) [][]string {
			plan := make([][]string, 0, len(images))
			for _, img := range images {
				plan = append(plan, dockercli.PullArgs(img, t.Platform))
			}
			return plan
		},
	}
}

// BuildObtainer 用 buildx 按目标架构构建镜像。
func BuildObtainer(base dockercli.BuildOptions) Obtainer {
	withPlatform := func(t *config.Target) dockercli.BuildOptions {
		o := base
		o.Platform = t.Platform
		return o
	}
	return Obtainer{
		Do: func(t *config.Target, _ []string) error {
			return dockercli.BuildxBuild(withPlatform(t))
		},
		Plan: func(t *config.Target, _ []string) [][]string {
			return [][]string{dockercli.BuildArgs(withPlatform(t))}
		},
	}
}

// LocalObtainer 只校验镜像已在本机且架构匹配, 不拉取也不构建。
func LocalObtainer() Obtainer {
	return Obtainer{
		Do: func(t *config.Target, images []string) error {
			for _, img := range images {
				got, err := dockercli.Inspect(img)
				if err != nil {
					return err
				}
				if got != t.Platform {
					return fmt.Errorf("本机镜像 %s 是 %s, 与环境 %q 的 %s 不匹配 (用 imgm build 重新构建)",
						img, got, t.Name, t.Platform)
				}
				fmt.Printf("-- 本机镜像 %s (%s) 就绪\n", img, got)
			}
			return nil
		},
		Plan: func(_ *config.Target, images []string) [][]string {
			plan := make([][]string, 0, len(images))
			for _, img := range images {
				plan = append(plan, []string{"image", "inspect", img})
			}
			return plan
		},
	}
}

// deployToHost 把已打好的 tar 上传到一台机器并执行导入。
// 上传的 tar 会留在远程机器上: 本工具不在别人的机器上删任何文件。
func deployToHost(out io.Writer, t *config.Target, h config.Host, localTar, remoteTar, importCmd string) error {
	client, err := remote.Dial(h)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Upload(localTar, remoteTar); err != nil {
		return err
	}

	fmt.Fprintf(out, "  导入: %s\n", importCmd)
	return client.Run(importCmd)
}

// tarName 生成形如 imgm-linux-amd64-3f9a1c04.tar 的文件名。
// 带随机后缀, 避免两个人同时往同一台机器部署时后者覆盖前者正在上传的包。
func tarName(platform string) string {
	var b [4]byte
	rand.Read(b[:])
	return fmt.Sprintf("imgm-%s-%s.tar", strings.ReplaceAll(platform, "/", "-"), hex.EncodeToString(b[:]))
}

// importCommand 根据环境类型生成远程导入命令。
func importCommand(t *config.Target, remoteTar string) string {
	switch t.Type {
	case config.TypeK8s:
		return fmt.Sprintf("ctr -n %s images import %s",
			remote.ShellQuote(t.Namespace), remote.ShellQuote(remoteTar))
	default:
		return fmt.Sprintf("docker load -i %s", remote.ShellQuote(remoteTar))
	}
}
