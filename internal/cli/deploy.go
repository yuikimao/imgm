package cli

import (
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"imgm/internal/config"
	"imgm/internal/dockercli"
	"imgm/internal/migrate"
)

// deployFlags 是 pull / build / push 共享的 flag。
type deployFlags struct {
	envs     []string
	all      bool
	platform string
	dryRun   bool
	yes      bool
	keepTar  bool
	workDir  string

	// overridden 记录 --platform 实际改掉了哪些环境的架构, 供确认横幅回显。
	overridden []string
}

func (f *deployFlags) register(fs *pflag.FlagSet) {
	fs.StringSliceVarP(&f.envs, "env", "e", nil, "目标环境, 逗号分隔可多个")
	fs.BoolVar(&f.all, "all", false, "发到所有环境")
	fs.StringVar(&f.platform, "platform", "", "本次的目标架构, 覆盖环境设置 (缺省跟随环境)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "只打印将要执行的动作, 不真的执行")
	fs.BoolVarP(&f.yes, "yes", "y", false, "跳过确认")
	fs.BoolVar(&f.keepTar, "keep-tar", false, "保留本地打好的 tar 包")
	fs.StringVar(&f.workDir, "work-dir", "", "tar 包输出目录 (缺省临时目录, 用完即删)")
}

// targets 解析出目标环境。-e 与 --all 二者必有其一且互斥。
func (f *deployFlags) targets() ([]*config.Target, error) {
	if f.all && len(f.envs) > 0 {
		return nil, fmt.Errorf("-e 与 --all 不能同时用")
	}
	if !f.all && len(f.envs) == 0 {
		return nil, fmt.Errorf("必须用 -e <环境> 指定目标环境 (或 --all 发到所有环境)")
	}
	if f.platform != "" {
		if err := config.ValidatePlatform(f.platform); err != nil {
			return nil, err
		}
	}
	cfg, err := mustLoad()
	if err != nil {
		return nil, err
	}
	targets, err := cfg.ResolveAll(f.envs, f.all)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if f.platform != "" && t.Platform != f.platform {
			f.overridden = append(f.overridden, fmt.Sprintf("%s (原 %s)", t.Name, t.Platform))
		}
		if f.platform != "" {
			t.Platform = f.platform
			t.PlatformIsDefault = false // 显式指定了就不必再提醒
		}
	}
	return targets, nil
}

// noticePlatformOverride 在 --platform 与环境设置不一致时说明本次是临时覆盖。
// 不提示的话, 用户会以为改的是环境配置, 下次不带 flag 就悄悄发错架构。
func (f *deployFlags) noticePlatformOverride(out io.Writer) {
	if len(f.overridden) == 0 {
		return
	}
	fmt.Fprintf(out, "ℹ 本次按 --platform %s 处理, 覆盖环境设置: %s\n",
		f.platform, strings.Join(f.overridden, ", "))
	fmt.Fprintf(out, "  只影响这一次; 要长期改用 imgm env set <环境> --platform %s\n\n", f.platform)
}

// noticeImplicitPlatform 在本机架构与将要使用的目标架构不同、且该架构只是
// 内置默认值时提醒一次。产物架构只由环境的 platform 决定, 与本机无关 —— Mac
// (arm64) 往 x86 服务器发时默认的 linux/amd64 通常是对的, 但如果目标机其实是
// ARM, 不说一句用户不会想到。
func noticeImplicitPlatform(out io.Writer, targets []*config.Target) {
	for _, t := range targets {
		if !t.PlatformIsDefault || runtime.GOARCH == archOf(t.Platform) {
			continue
		}
		fmt.Fprintf(out, "ℹ 环境 %s 没设架构, 按默认的 %s 拉取和打包 (与本机 %s 无关, 只看目标机)\n",
			t.Name, t.Platform, runtime.GOARCH)
		fmt.Fprintf(out, "  目标机若是 ARM: imgm env set %s --platform linux/arm64\n\n", t.Name)
	}
}

// archOf 取 platform 的架构段, linux/amd64 -> amd64。
func archOf(platform string) string {
	if i := strings.LastIndex(platform, "/"); i >= 0 {
		return platform[i+1:]
	}
	return platform
}

// confirm 在上传第一个字节前展示完整影响面。没有"当前环境"概念,
// 这个横幅就是防止发错环境的唯一屏障。
func (f *deployFlags) confirm(out io.Writer, verb string, targets []*config.Target, images []string) error {
	noticeImplicitPlatform(out, targets)
	f.noticePlatformOverride(out)
	if f.dryRun || f.yes {
		return nil
	}

	hosts := 0
	for _, t := range targets {
		hosts += len(t.Hosts)
	}
	fmt.Fprintf(out, "即将%s %d 个镜像到 %d 个环境 / %d 台机器:\n", verb, len(images), len(targets), hosts)
	fmt.Fprintf(out, "  %s\n", strings.Join(images, ", "))
	for _, t := range targets {
		addrs := make([]string, 0, len(t.Hosts))
		for _, h := range t.Hosts {
			addrs = append(addrs, h.Host)
		}
		fmt.Fprintf(out, "  %s (%s, %s) → %s\n", t.Name, t.Type, t.Platform, strings.Join(addrs, ", "))
	}

	ok, err := NewPrompter().Confirm("继续?")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("已取消")
	}
	return nil
}

// run 执行部署。--work-dir 隐含保留 tar: 在用户指定的目录里自动删文件是敌意行为。
func (f *deployFlags) run(cmd *cobra.Command, targets []*config.Target, images []string, obtain migrate.Obtainer) error {
	_, err := migrate.Deploy(targets, migrate.Options{
		Images:  images,
		Obtain:  obtain,
		WorkDir: f.workDir,
		KeepTar: f.keepTar || f.workDir != "",
		DryRun:  f.dryRun,
		Out:     cmd.OutOrStdout(),
	})
	return err
}

func newPullCmd() *cobra.Command {
	var f deployFlags
	cmd := &cobra.Command{
		Use:   "pull -e <env> <image>...",
		Short: "从 registry 拉取镜像并分发到环境所有机器",
		Long: `按环境的目标架构从 registry 拉取镜像, 打包上传到该环境每台机器并导入。

同架构的多个环境只拉取和打包一次。`,
		Example: `  imgm pull -e prod nginx:1.25 redis:7.0
  imgm pull -e prod,test nginx:1.25
  imgm pull --all nginx:1.25 --dry-run`,
		Args: wantArgs(1, "没有指定要拉取的镜像"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := f.targets()
			if err != nil {
				return err
			}
			if err := f.confirm(cmd.ErrOrStderr(), "拉取并部署", targets, args); err != nil {
				return err
			}
			return f.run(cmd, targets, args, migrate.PullObtainer())
		},
	}
	f.register(cmd.Flags())
	return cmd
}

func newPushCmd() *cobra.Command {
	var f deployFlags
	cmd := &cobra.Command{
		Use:   "push -e <env> <image>...",
		Short: "把本机已有的镜像分发到环境所有机器",
		Long: `把本机 docker 里已有的镜像打包上传并导入。不拉取也不构建,
上传前会校验镜像存在且架构与环境一致。`,
		Example: `  imgm push -e prod myapp:1.0`,
		Args:    wantArgs(1, "没有指定要分发的镜像"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := f.targets()
			if err != nil {
				return err
			}
			if err := f.confirm(cmd.ErrOrStderr(), "部署本机", targets, args); err != nil {
				return err
			}
			return f.run(cmd, targets, args, migrate.LocalObtainer())
		},
	}
	f.register(cmd.Flags())
	return cmd
}

func newBuildCmd() *cobra.Command {
	var (
		f          deployFlags
		tag        string
		dockerfile string
		buildCtx   string
		outTar     string
	)
	cmd := &cobra.Command{
		Use:   "build -t <image:tag> [-e <env>] [context]",
		Short: "用 buildx 构建镜像, 可直接分发到环境",
		Long: `用 docker buildx 构建镜像 (可跨架构)。

带 -e: 按该环境的架构构建, 并直接分发到其所有机器。
不带 -e: 只在本机构建; 加 -o 可导出成 tar 手动带走。

--platform 覆盖本次的目标架构 (带 -e 时不改环境配置, 只影响这一次)。`,
		Example: `  imgm build -e prod -t myapp:1.0 .
  imgm build -e prod -t myapp:1.0 --platform linux/arm64 .
  imgm build -t myapp:1.0 --platform linux/amd64 -o myapp.tar .`,
		Args: wantAtMostArgs(1, "构建上下文只能给一个目录"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				buildCtx = args[0]
			}
			opts := dockercli.BuildOptions{
				Tag:        tag,
				Dockerfile: dockerfile,
				Context:    buildCtx,
				Platform:   f.platform,
				OutputTar:  outTar,
			}

			// 不带 -e/--all: 纯本地构建, 这是"构建个 tar 拷走"的逃生口。
			if !f.all && len(f.envs) == 0 {
				if opts.Platform == "" {
					opts.Platform = config.DefaultPlatform
				} else if err := config.ValidatePlatform(opts.Platform); err != nil {
					return err
				}
				if err := dockercli.BuildxBuild(opts); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "构建完成。")
				return nil
			}

			// 分发需要镜像加载到本机 docker, 与导出 tar 互斥。
			if outTar != "" {
				return fmt.Errorf("-o 导出 tar 与 -e 分发不能同用 (分发需要镜像先加载到本机 docker)")
			}
			targets, err := f.targets()
			if err != nil {
				return err
			}

			if err := f.confirm(cmd.ErrOrStderr(), "构建并部署", targets, []string{tag}); err != nil {
				return err
			}
			return f.run(cmd, targets, []string{tag}, migrate.BuildObtainer(opts))
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&tag, "tag", "t", "", "镜像 tag, 如 myapp:1.0 (必填)")
	fs.StringVarP(&dockerfile, "file", "f", "", "Dockerfile 路径 (缺省 <context>/Dockerfile)")
	fs.StringVar(&buildCtx, "context", ".", "构建上下文目录")
	fs.StringVarP(&outTar, "output", "o", "", "导出 tar 路径 (不能与 -e 同用)")
	f.register(fs)
	cmd.MarkFlagRequired("tag")
	return cmd
}
