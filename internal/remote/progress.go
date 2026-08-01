package remote

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// progressReader 包住源 Reader, 按读取字节数刷新一行进度。
//
// 包源而不是包目标: io.Copy 会走 sftp.File 的 ReadFrom 快路径 (按包切分、
// 可并发), 包住目标 Writer 会让它退化成逐次 Write。读取只发生在写周期里,
// 所以读到的字节数最多领先实际发出的一个包, 对进度显示足够准。
type progressReader struct {
	src   io.Reader
	total int64
	done  int64

	out      io.Writer
	tty      bool
	start    time.Time
	lastDraw time.Time
	lastLen  int

	// 瞬时速率 (B/s) 及其采样基准。speed 是指数滑动平均后的值。
	speed     float64
	lastBytes int64
}

const (
	ttyRedrawEvery  = 200 * time.Millisecond
	fileRedrawEvery = 5 * time.Second

	// 新采样占的权重。偏小以求平滑 —— 链路抖动时数字乱跳比不显示更糟。
	ewmaAlpha = 0.3
)

func newProgressReader(src io.Reader, total int64, out io.Writer) *progressReader {
	return &progressReader{
		src:   src,
		total: total,
		out:   out,
		tty:   isTerminal(out),
		start: time.Now(),
	}
}

// Size 让 sftp.File.ReadFrom 能判断出总长度以选择并发度。
// 包装后如果丢了这个方法, 上传会退回单请求串行。
func (r *progressReader) Size() int64 { return r.total }

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	r.done += int64(n)

	every := fileRedrawEvery
	if r.tty {
		every = ttyRedrawEvery
	}
	if time.Since(r.lastDraw) >= every {
		r.draw()
	}
	return n, err
}

// finish 收尾: 换成总耗时与平均速率, 这两个数才是复盘时想看的。
func (r *progressReader) finish() {
	if r.tty {
		// 擦掉最后一帧实时进度, 避免和总结行叠在一起。
		fmt.Fprintf(r.out, "\r%s\r", strings.Repeat(" ", r.lastLen))
	}
	elapsed := time.Since(r.start)
	avg := int64(0)
	if s := elapsed.Seconds(); s > 0 {
		avg = int64(float64(r.done) / s)
	}
	fmt.Fprintf(r.out, "  已传 %s, 耗时 %s, 平均 %s/s\n",
		humanBytes(r.done), humanDuration(elapsed), humanBytes(avg))
}

// sample 更新瞬时速率。用指数滑动平均抹掉抖动 —— 链路忙的时候原始采样
// 会在 0 和峰值之间跳, 直接显示没法看。
func (r *progressReader) sample(now time.Time) {
	if r.lastDraw.IsZero() {
		r.lastDraw, r.lastBytes = now, r.done
		return
	}
	dt := now.Sub(r.lastDraw).Seconds()
	if dt <= 0 {
		return
	}
	cur := float64(r.done-r.lastBytes) / dt
	if r.speed == 0 {
		r.speed = cur
	} else {
		r.speed = ewmaAlpha*cur + (1-ewmaAlpha)*r.speed
	}
	r.lastDraw, r.lastBytes = now, r.done
}

func (r *progressReader) draw() {
	r.sample(time.Now())

	pct := 0.0
	if r.total > 0 {
		pct = float64(r.done) / float64(r.total) * 100
	}

	// 慢到几乎不动时给 ETA 会算出天文数字, 不如直接说不知道。
	eta := "--"
	if r.speed > 1024 && r.total > r.done {
		eta = humanDuration(time.Duration(float64(r.total-r.done)/r.speed) * time.Second)
	}

	line := fmt.Sprintf("  %s %5.1f%%  %s / %s  %s/s  剩余 %s",
		bar(pct), pct, humanBytes(r.done), humanBytes(r.total),
		humanBytes(int64(r.speed)), eta)

	if !r.tty {
		fmt.Fprintln(r.out, line)
		return
	}
	// 新行比旧行短时补空格, 否则会留下上一行的尾巴。
	pad := ""
	if n := r.lastLen - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	r.lastLen = len(line)
	fmt.Fprintf(r.out, "\r%s%s", line, pad)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

const barWidth = 24

func bar(pct float64) string {
	filled := int(pct / 100 * barWidth)
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled) + "]"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
