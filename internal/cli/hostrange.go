package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// maxRangeHosts 限制一次区间展开的台数, 防止手滑打成 10.0.0.1-255 之类。
const maxRangeHosts = 256

var (
	// hostRangeRe 只认 10.0.0.11-14 这种"末段区间"写法。要求三个点分数字组,
	// 所以 node-1 / web-server-3 这类含连字符的主机名永远不会被误展开。
	hostRangeRe = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})-(\d{1,3})$`)
	// fullPairRe 只用来给 10.0.0.11-10.0.0.14 这种写法一个明确的报错。
	fullPairRe = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}-\d{1,3}(\.\d{1,3}){3}$`)
)

// rangeExpansion 记录一次展开, 用于向用户回显"你写的这个变成了哪几台"。
type rangeExpansion struct {
	From string
	To   []string
}

// expandHostRange 展开 10.0.0.11-14 形式的地址。不是区间写法的原样返回。
func expandHostRange(s string) ([]string, error) {
	if fullPairRe.MatchString(s) {
		return nil, fmt.Errorf("区间 %q 请写成 10.0.0.11-14 (只写结束段)", s)
	}
	m := hostRangeRe.FindStringSubmatch(s)
	if m == nil {
		return []string{s}, nil
	}

	// 前导零会让 10.0.0.01-04 和 10.0.0.1-4 展开成同一批, 最后在去重那里
	// 报一个跟输入对不上的"已有机器", 不如在这里直接拦掉。
	for _, seg := range m[1:] {
		if len(seg) > 1 && seg[0] == '0' {
			return nil, fmt.Errorf("区间 %q 非法: 不要用前导零", s)
		}
	}

	nums := make([]int, 5)
	for i, seg := range m[1:] {
		n, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("区间 %q 非法: %w", s, err)
		}
		if n > 255 {
			return nil, fmt.Errorf("区间 %q 非法: 网段 %d 超出 0-255", s, n)
		}
		nums[i] = n
	}

	start, end := nums[3], nums[4]
	if end < start {
		return nil, fmt.Errorf("区间 %q 非法: 结束值 %d 小于起始值 %d", s, end, start)
	}
	if n := end - start + 1; n > maxRangeHosts {
		return nil, fmt.Errorf("区间 %q 一次展开 %d 台, 超过上限 %d, 请拆分", s, n, maxRangeHosts)
	}

	out := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], i))
	}
	return out, nil
}

// expandHostList 规整一批用户输入的地址: 先按逗号切分, 再展开区间简写。
// 同时返回哪几项发生了展开 (用于回显)。
func expandHostList(in []string) ([]string, []rangeExpansion, error) {
	out := make([]string, 0, len(in))
	var expanded []rangeExpansion
	for _, raw := range in {
		for _, s := range splitList(raw) {
			hosts, err := expandHostRange(s)
			if err != nil {
				return nil, nil, err
			}
			if len(hosts) > 1 {
				expanded = append(expanded, rangeExpansion{From: s, To: hosts})
			}
			out = append(out, hosts...)
		}
	}
	return out, expanded, nil
}

// describeExpansions 把展开结果渲染成人话, 供确认横幅与向导回显共用。
func describeExpansions(exps []rangeExpansion) string {
	var b strings.Builder
	for _, e := range exps {
		fmt.Fprintf(&b, "  %s 展开为 %d 台: %s\n", e.From, len(e.To), strings.Join(e.To, ", "))
	}
	return b.String()
}
