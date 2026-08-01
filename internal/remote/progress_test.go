package remote

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		512:                    "512 B",
		1024:                   "1.0 KiB",
		70 * 1024 * 1024:       "70.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/tmp":                  "'/tmp'",
		"/tmp/a b":              "'/tmp/a b'",
		"/tmp'; rm -rf /; echo": `'/tmp'\''; rm -rf /; echo'`,
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// readerFromSink 模拟 sftp.File: 实现 ReaderFrom, 并记录它是否能从
// 传入的 reader 上探测到 Size()。丢了 Size() 会让 sftp 退回串行上传。
type readerFromSink struct {
	sawSize int64
	n       int64
}

func (s *readerFromSink) Write(p []byte) (int, error) { return len(p), nil }

func (s *readerFromSink) ReadFrom(r io.Reader) (int64, error) {
	if sz, ok := r.(interface{ Size() int64 }); ok {
		s.sawSize = sz.Size()
	}
	n, err := io.Copy(struct{ io.Writer }{s}, r)
	s.n = n
	return n, err
}

func TestProgressReaderPreservesReadFromFastPath(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 4096)
	pr := newProgressReader(bytes.NewReader(data), int64(len(data)), io.Discard)

	sink := &readerFromSink{}
	if _, err := io.Copy(sink, pr); err != nil {
		t.Fatal(err)
	}

	if sink.sawSize != int64(len(data)) {
		t.Errorf("ReadFrom 没能探测到 Size(): got %d, want %d", sink.sawSize, len(data))
	}
	if sink.n != int64(len(data)) {
		t.Errorf("传输字节数 = %d, want %d", sink.n, len(data))
	}
	if pr.done != int64(len(data)) {
		t.Errorf("进度计数 = %d, want %d", pr.done, len(data))
	}
}

func TestProgressReaderNonTTYOutput(t *testing.T) {
	data := bytes.Repeat([]byte("y"), 1024)
	var buf bytes.Buffer
	pr := newProgressReader(bytes.NewReader(data), int64(len(data)), &buf)

	if _, err := io.Copy(io.Discard, pr); err != nil {
		t.Fatal(err)
	}
	pr.finish()

	out := buf.String()
	if strings.Contains(out, "\r") {
		t.Errorf("非 TTY 输出不应含回车符: %q", out)
	}
	if !strings.Contains(out, "100.0%") {
		t.Errorf("最终输出应含 100.0%%: %q", out)
	}
}

func TestIsTerminalOnNonFile(t *testing.T) {
	if isTerminal(io.Discard) {
		t.Error("io.Discard 不应被判定为终端")
	}
	if isTerminal(&bytes.Buffer{}) {
		t.Error("bytes.Buffer 不应被判定为终端")
	}
	// devNull 是 *os.File 但不是终端, 确认没有误判成 tty。
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer devNull.Close()
	if isTerminal(devNull) {
		t.Error("/dev/null 不应被判定为终端")
	}
}
