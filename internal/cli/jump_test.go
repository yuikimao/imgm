package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"imgm/internal/config"
)

// newTestPrompter 造一个假装是 tty 的 Prompter, 好驱动交互分支。
func newTestPrompter(input string) (*Prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return &Prompter{
		in:  bufio.NewReader(strings.NewReader(input)),
		out: &out,
		tty: true,
	}, &out
}

func TestAskJumpAcceptsListedHost(t *testing.T) {
	p, _ := newTestPrompter("10.0.0.2\n")
	got, err := askJump(p, []string{"10.0.0.1", "10.0.0.2"})
	if err != nil {
		t.Fatalf("askJump: %v", err)
	}
	if got != "10.0.0.2" {
		t.Errorf("got %q, want 10.0.0.2", got)
	}
}

// 直接回车 = 都能直连。Line 会把空输入当必填无限重问, 所以这条是 LineOptional 的意义所在。
func TestAskJumpEmptyMeansNoJump(t *testing.T) {
	p, _ := newTestPrompter("\n")
	got, err := askJump(p, []string{"10.0.0.1", "10.0.0.2"})
	if err != nil {
		t.Fatalf("askJump: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want 空串", got)
	}
}

func TestAskJumpRepromptsOnUnknownHost(t *testing.T) {
	p, out := newTestPrompter("10.9.9.9\n10.0.0.1\n")
	got, err := askJump(p, []string{"10.0.0.1", "10.0.0.2"})
	if err != nil {
		t.Fatalf("askJump: %v", err)
	}
	if got != "10.0.0.1" {
		t.Errorf("got %q, want 10.0.0.1", got)
	}
	if !strings.Contains(out.String(), "不在上面的机器里") {
		t.Errorf("应提示地址不在列表里, 实际输出: %q", out.String())
	}
}

func TestLineOptionalNonTTY(t *testing.T) {
	p := &Prompter{in: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}, tty: false}
	got, err := p.LineOptional("跳板机")
	if err != nil {
		t.Fatalf("LineOptional: %v", err)
	}
	if got != "" {
		t.Errorf("非交互环境应返回空串, 得到 %q", got)
	}
}

func TestCheckJumpMember(t *testing.T) {
	addrs := []string{"10.0.0.1", "10.0.0.2"}
	if err := checkJumpMember("10.0.0.1", addrs, "prod"); err != nil {
		t.Errorf("跳板机在列表里, 不该报错: %v", err)
	}
	if err := checkJumpMember("", addrs, "prod"); err != nil {
		t.Errorf("空值表示不用跳板机, 不该报错: %v", err)
	}
	// env add 允许先建没有机器的空骨架, 之后再 host add 补上。
	if err := checkJumpMember("10.0.0.1", nil, "prod"); err != nil {
		t.Errorf("环境还没有机器时不该拦: %v", err)
	}
	err := checkJumpMember("10.9.9.9", addrs, "prod")
	if err == nil {
		t.Fatal("跳板机不在列表里, 应该报错")
	}
	if !strings.Contains(err.Error(), "10.0.0.1, 10.0.0.2") {
		t.Errorf("报错应列出可选地址, 实际: %v", err)
	}
}

func TestNoteFor(t *testing.T) {
	cases := []struct {
		jump bool
		mark hostMark
		want string
	}{
		{false, markNone, ""},
		{false, markAdded, "  ← 新增"},
		{true, markNone, "  ← 跳板机"},
		{true, markChanged, "  ← 跳板机, 已改"},
	}
	for _, c := range cases {
		if got := noteFor(c.jump, c.mark); got != c.want {
			t.Errorf("noteFor(%v, %v) = %q, want %q", c.jump, c.mark, got, c.want)
		}
	}
}

// okProbes / deadProbes 造两种典型的探测结果。
func okProbes() []probe   { return []probe{{statusOK, "SSH 连接", "1ms"}} }
func deadProbes() []probe { return []probe{{statusFail, "SSH 连接", "i/o timeout"}} }

func suggestTarget(addrs ...string) *config.Target {
	t := &config.Target{Name: "prod"}
	for _, a := range addrs {
		t.Hosts = append(t.Hosts, config.Host{Host: a})
	}
	return t
}

// 中文按两格算, 否则含 <密码> 的那几行会跟纯 ASCII 行错开。
func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"--password ''", 13},
		{"--password '<密码>'", 19},
	}
	for _, c := range cases {
		if got := displayWidth(c.in); got != c.want {
			t.Errorf("displayWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if got := padCJK("<密码>", 10); displayWidth(got) != 10 {
		t.Errorf("padCJK 后显示宽度应为 10, 得到 %d (%q)", displayWidth(got), got)
	}
}

// 例子用用户自己的网段, 不然 10.0.0.x 看着像文档没换过。
func TestExampleAddr(t *testing.T) {
	cases := []struct {
		hosts []config.Host
		want  string
	}{
		{nil, "10.0.0.20"},
		{[]config.Host{{Host: "172.20.2.132"}}, "172.20.2.20"},
		{[]config.Host{{Host: "192.168.30.11"}, {Host: "10.0.0.1"}}, "192.168.30.20"},
		{[]config.Host{{Host: "node-1"}}, "10.0.0.20"},
	}
	for _, c := range cases {
		if got := exampleAddr(c.hosts); got != c.want {
			t.Errorf("exampleAddr(%v) = %q, want %q", c.hosts, got, c.want)
		}
	}
}

func TestSuggestJumpOnSingleReachable(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2", "10.0.0.3")
	got := suggestJump(tgt, [][]probe{okProbes(), deadProbes(), deadProbes()})
	if !strings.Contains(got, "imgm env set prod --jump 10.0.0.1") {
		t.Errorf("应给出设置跳板机的命令, 实际: %q", got)
	}
	if !strings.Contains(got, "其余 2 台") {
		t.Errorf("应说明有几台要中转, 实际: %q", got)
	}
}

// 全通 = 不需要跳板机, 多提一句只会让人以为该设。
func TestSuggestJumpSilentWhenAllReachable(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2")
	if got := suggestJump(tgt, [][]probe{okProbes(), okProbes()}); got != "" {
		t.Errorf("全部连得上时不该提示, 实际: %q", got)
	}
}

// 两台连得上一台不通, 更像那一台自己挂了, 不是跳板机拓扑。
func TestSuggestJumpSilentWhenSeveralReachable(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2", "10.0.0.3")
	got := suggestJump(tgt, [][]probe{okProbes(), okProbes(), deadProbes()})
	if got != "" {
		t.Errorf("不止一台连得上时不该提示, 实际: %q", got)
	}
}

func TestSuggestJumpSilentWhenAllDead(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2")
	if got := suggestJump(tgt, [][]probe{deadProbes(), deadProbes()}); got != "" {
		t.Errorf("全都连不上时无从推荐, 实际: %q", got)
	}
}

func TestSuggestJumpSilentWhenAlreadySet(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2")
	tgt.Jump = &tgt.Hosts[0]
	got := suggestJump(tgt, [][]probe{okProbes(), deadProbes()})
	if got != "" {
		t.Errorf("已经设过跳板机时不该再提示, 实际: %q", got)
	}
}

// 连上了但 docker 没装, 换跳板机也救不了, 不该算「不可达」。
func TestSuggestJumpIgnoresNonDialFailures(t *testing.T) {
	tgt := suggestTarget("10.0.0.1", "10.0.0.2")
	broken := []probe{{statusOK, "SSH 连接", "1ms"}, {statusFail, "运行时 docker", "未安装"}}
	if got := suggestJump(tgt, [][]probe{okProbes(), broken}); got != "" {
		t.Errorf("非拨号失败不该触发跳板机建议, 实际: %q", got)
	}
}
