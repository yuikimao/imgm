package dockercli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// run 执行一条 docker 命令，把 stdout/stderr 透传到当前进程，便于看进度。
func run(args ...string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %v 执行失败: %w", args, err)
	}
	return nil
}

// Pull 按指定平台拉取镜像。platform 为空则不带 --platform。
func Pull(image, platform string) error {
	return run(PullArgs(image, platform)...)
}

// PullArgs 返回 Pull 会执行的 docker 参数, 供 --dry-run 打印。
func PullArgs(image, platform string) []string {
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	return append(args, image)
}

// Save 把多个镜像打包到一个 tar 文件。
// platform 非空时只导出该平台 (containerd 镜像存储下跨架构 save 必需, 否则会因
// 缺少其它平台的 blob 报 "content digest not found")。
func Save(images []string, outPath, platform string) error {
	return run(SaveArgs(images, outPath, platform)...)
}

// SaveArgs 返回 Save 会执行的 docker 参数, 供 --dry-run 打印。
func SaveArgs(images []string, outPath, platform string) []string {
	args := []string{"save", "-o", outPath}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	return append(args, images...)
}

// Inspect 返回本机镜像的 os/arch (如 linux/amd64)。镜像不存在时返回错误。
// 用于 push 前预检, 避免直接死在 docker save 的 "content digest not found" 上。
func Inspect(image string) (string, error) {
	out, err := exec.Command("docker", "image", "inspect", "-f", "{{.Os}}/{{.Architecture}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("本机没有镜像 %s (先 docker pull 或 imgm build)", image)
	}
	return strings.TrimSpace(string(out)), nil
}

// BuildOptions 描述一次 buildx 构建。
type BuildOptions struct {
	Tag        string // -t
	Dockerfile string // -f，可空（默认 <context>/Dockerfile）
	Platform   string // --platform，如 linux/amd64
	Context    string // 构建上下文目录，默认 "."
	OutputTar  string // 非空则直接导出 tar；空则 --load 到本地 docker
}

// BuildxBuild 用 docker buildx 构建（可跨架构）。
func BuildxBuild(opts BuildOptions) error {
	if opts.Tag == "" {
		return fmt.Errorf("构建失败: 必须指定镜像 tag")
	}
	return run(BuildArgs(opts)...)
}

// BuildArgs 返回 BuildxBuild 会执行的 docker 参数, 供 --dry-run 打印。
func BuildArgs(opts BuildOptions) []string {
	ctx := opts.Context
	if ctx == "" {
		ctx = "."
	}

	args := []string{"buildx", "build"}
	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	}
	args = append(args, "-t", opts.Tag)
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	if opts.OutputTar != "" {
		args = append(args, "--output", "type=docker,dest="+opts.OutputTar)
	} else {
		args = append(args, "--load")
	}
	return append(args, ctx)
}
