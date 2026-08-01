package remote

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"imgm/internal/config"
)

// Client 封装一个到目标机器的 SSH 连接及其 SFTP 子系统。
type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

// Dial 建立 SSH 连接, 使用默认超时。
func Dial(cfg config.Host) (*Client, error) {
	return DialTimeout(cfg, 15*time.Second)
}

// DialTimeout 建立 SSH 连接。私钥优先，回退密码。
func DialTimeout(cfg config.Host, timeout time.Duration) (*Client, error) {
	var authMethods []ssh.AuthMethod

	if cfg.KeyFile != "" {
		key, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("读取私钥 %s 失败: %w", cfg.KeyFile, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析私钥 %s 失败: %w", cfg.KeyFile, err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(cfg.Password))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("未提供任何 SSH 认证方式")
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 内网迁移工具，忽略主机指纹校验
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	sshClient, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接 %s 失败: %w", addr, err)
	}

	// 默认 SFTP 每个包都要等一次往返确认, 70 MiB 就是两千多次串行 RTT ——
	// 跨机房时能慢到几百 KiB/s。开并发写后按 RTT 并行流水线, 大文件快一个
	// 数量级。包体保持默认 32 KiB: 更大的包不是所有 SFTP 服务端都收。
	//
	// 并发写的代价: 中途出错时高偏移的写可能已经落盘, 留下带空洞的文件。
	// Upload 出错时会明确告知远端文件不完整 (本工具不删远端文件)。
	sftpClient, err := sftp.NewClient(sshClient,
		sftp.UseConcurrentWrites(true),
		sftp.MaxConcurrentRequestsPerFile(64),
	)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("创建 SFTP 会话失败: %w", err)
	}

	return &Client{ssh: sshClient, sftp: sftpClient}, nil
}

// Run 在远程执行一条命令，输出透传到当前进程。
func (c *Client) Run(cmd string) error {
	session, err := c.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("创建 SSH session 失败: %w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("远程命令执行失败 [%s]: %w", cmd, err)
	}
	return nil
}

// Output 在远程执行一条命令并捕获其输出 (含 stderr), 供探测类命令使用。
func (c *Client) Output(cmd string) (string, error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH session 失败: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	return strings.TrimSpace(string(out)), err
}

// Upload 通过 SFTP 上传本地文件到远程路径。
func (c *Client) Upload(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件 %s 失败: %w", localPath, err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("获取本地文件信息失败: %w", err)
	}

	// 只检查目录存在, 不创建 —— 不在别人的机器上建目录。
	if dir := parentDir(remotePath); dir != "" {
		fi, err := c.sftp.Stat(dir)
		if err != nil {
			return fmt.Errorf("远程目录 %s 不存在或不可访问 (用 imgm env set <环境> --remote-tmp 换一个已有目录): %w", dir, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("远程路径 %s 不是目录", dir)
		}
	}

	dst, err := c.sftp.Create(remotePath)
	if err != nil {
		return fmt.Errorf("创建远程文件 %s 失败: %w", remotePath, err)
	}
	defer dst.Close()

	fmt.Fprintf(os.Stderr, "  上传 %s -> %s (%s)\n", localPath, remotePath, humanBytes(info.Size()))
	pr := newProgressReader(src, info.Size(), os.Stderr)
	_, err = io.Copy(dst, pr)
	pr.finish()
	if err != nil {
		// 并发写下中断会留下长度对不上的残缺文件, 但本工具不删远端文件,
		// 只能说清楚 —— 否则下次 docker load 会报难懂的 tar 解析错误。
		return fmt.Errorf("上传文件失败: %w\n  远端 %s 可能是残缺的, 重传前请自行删除", err, remotePath)
	}
	return nil
}

// ShellQuote 把字符串包成一个 POSIX shell 单引号字面量。
// 单引号内除了单引号本身没有任何元字符会被解释, 所以只需把内部的 ' 拆成
// '\” 收尾再续上。凡是要拼进远程命令的路径都必须过这里。
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Close 关闭 SFTP 与 SSH 连接。
func (c *Client) Close() error {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.ssh != nil {
		return c.ssh.Close()
	}
	return nil
}

// parentDir 返回远程路径（POSIX 风格）的父目录。
func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return ""
}
