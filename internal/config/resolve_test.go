package config

import "testing"

// jumpEnv 造一个带跳板机的三机环境。跳板机自己也是部署目标。
func jumpEnv() *Config {
	return &Config{
		Environments: []Environment{{
			Name: "prod",
			Type: TypeDocker,
			Jump: "10.0.0.1",
			SSH:  SSHParams{User: "root", Password: "pw"},
			Hosts: []Host{
				{Host: "10.0.0.1"},
				{Host: "10.0.0.2"},
				{Host: "10.0.0.3"},
			},
		}},
	}
}

func TestResolveJumpPointsIntoHosts(t *testing.T) {
	got, err := jumpEnv().Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Jump == nil {
		t.Fatal("Jump 为 nil, 应指向 10.0.0.1")
	}
	if got.Jump.Host != "10.0.0.1" {
		t.Errorf("Jump.Host = %q, want 10.0.0.1", got.Jump.Host)
	}
	// 必须指向 Hosts 里那一台, 且凭据已经继承完毕。
	if got.Jump != &got.Hosts[0] {
		t.Error("Jump 应指向 Hosts[0] 本身, 而不是一份拷贝")
	}
	if got.Jump.User != "root" || got.Jump.Password != "pw" || got.Jump.Port != DefaultSSHPort {
		t.Errorf("跳板机凭据没继承完整: %+v", *got.Jump)
	}
}

func TestResolveRejectsJumpNotInHosts(t *testing.T) {
	cfg := jumpEnv()
	cfg.Environments[0].Jump = "10.9.9.9"
	if _, err := cfg.Resolve("prod"); err == nil {
		t.Fatal("跳板机不在机器列表里, 应该报错")
	}
}

func TestResolveNoJump(t *testing.T) {
	cfg := jumpEnv()
	cfg.Environments[0].Jump = ""
	got, err := cfg.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Jump != nil {
		t.Error("没设跳板机时 Jump 应为 nil")
	}
	if got.NeedsJump() {
		t.Error("没设跳板机时 NeedsJump 应为 false")
	}
	if got.JumpFor(got.Hosts[1]) != nil {
		t.Error("没设跳板机时 JumpFor 应为 nil")
	}
}

func TestJumpFor(t *testing.T) {
	got, err := jumpEnv().Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 跳板机连它自己是直连, 不该绕着自己兜一圈。
	if v := got.JumpFor(got.Hosts[0]); v != nil {
		t.Errorf("跳板机自己应直连, 得到 %v", v)
	}
	for _, i := range []int{1, 2} {
		v := got.JumpFor(got.Hosts[i])
		if v == nil || v.Host != "10.0.0.1" {
			t.Errorf("Hosts[%d] 应经 10.0.0.1 中转, 得到 %v", i, v)
		}
	}
}

func TestNeedsJumpOnlyBastion(t *testing.T) {
	cfg := jumpEnv()
	cfg.Environments[0].Hosts = []Host{{Host: "10.0.0.1"}}
	got, err := cfg.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Jump == nil {
		t.Fatal("Jump 不该为 nil")
	}
	if got.NeedsJump() {
		t.Error("只有跳板机一台时不必建隧道")
	}
}

// ValidateEnv 只查语法。归属由 Resolve 判断 —— env ls 会对每个环境调用
// ValidateEnv, 不该因为机器列表而报错。
func TestValidateEnvIgnoresJumpMembership(t *testing.T) {
	e := &Environment{Name: "prod", Type: TypeDocker, Jump: "10.9.9.9"}
	if err := ValidateEnv(e); err != nil {
		t.Errorf("ValidateEnv 不该查归属: %v", err)
	}
	e.Jump = "bad addr/with spaces"
	if err := ValidateEnv(e); err == nil {
		t.Error("跳板机地址非法时应该报错")
	}
}

func TestRenameHostFollowsJump(t *testing.T) {
	cfg := jumpEnv()
	if err := cfg.RenameHost("prod", "10.0.0.1", "10.0.1.9"); err != nil {
		t.Fatalf("RenameHost: %v", err)
	}
	if got := cfg.FindEnv("prod").Jump; got != "10.0.1.9" {
		t.Errorf("Jump = %q, 改地址后应跟着变成 10.0.1.9", got)
	}
	if _, err := cfg.Resolve("prod"); err != nil {
		t.Errorf("改完地址后 Resolve 应该还能通: %v", err)
	}
}

func TestRenameNonJumpHostLeavesJump(t *testing.T) {
	cfg := jumpEnv()
	if err := cfg.RenameHost("prod", "10.0.0.2", "10.0.1.9"); err != nil {
		t.Fatalf("RenameHost: %v", err)
	}
	if got := cfg.FindEnv("prod").Jump; got != "10.0.0.1" {
		t.Errorf("Jump = %q, 改别台机器不该动跳板机", got)
	}
}

func TestRemoveHostRefusesJumpInUse(t *testing.T) {
	cfg := jumpEnv()
	if err := cfg.RemoveHost("prod", "10.0.0.1"); err == nil {
		t.Fatal("其余机器还要靠它中转, 应该拒绝删跳板机")
	}
	if len(cfg.FindEnv("prod").Hosts) != 3 {
		t.Error("拒绝之后不该动机器列表")
	}
}

func TestRemoveHostAllowsLastJump(t *testing.T) {
	cfg := jumpEnv()
	for _, addr := range []string{"10.0.0.2", "10.0.0.3"} {
		if err := cfg.RemoveHost("prod", addr); err != nil {
			t.Fatalf("RemoveHost %s: %v", addr, err)
		}
	}
	// 没人依赖它了, 可以删, 并且要顺手清掉悬空的引用。
	if err := cfg.RemoveHost("prod", "10.0.0.1"); err != nil {
		t.Fatalf("只剩跳板机自己时应允许删除: %v", err)
	}
	if got := cfg.FindEnv("prod").Jump; got != "" {
		t.Errorf("Jump = %q, 删掉跳板机后应清空", got)
	}
}
