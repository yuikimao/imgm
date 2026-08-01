package config

import "fmt"

// FindEnv 按名字返回环境指针, 不存在则返回 nil。返回的是切片元素指针, 可直接改。
func (c *Config) FindEnv(name string) *Environment {
	for i := range c.Environments {
		if c.Environments[i].Name == name {
			return &c.Environments[i]
		}
	}
	return nil
}

// AddEnv 追加一个新环境。字段非法或同名则报错。
func (c *Config) AddEnv(e Environment) error {
	if err := ValidateEnv(&e); err != nil {
		return err
	}
	if c.FindEnv(e.Name) != nil {
		return fmt.Errorf("环境 %q 已存在 (imgm env show %s 查看, 或换个名字)", e.Name, e.Name)
	}
	for i := range e.Hosts {
		if err := ValidateHost(&e.Hosts[i]); err != nil {
			return err
		}
	}
	c.Environments = append(c.Environments, e)
	return nil
}

// RemoveEnv 删除指定环境。
func (c *Config) RemoveEnv(name string) error {
	for i := range c.Environments {
		if c.Environments[i].Name == name {
			c.Environments = append(c.Environments[:i], c.Environments[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("不存在名为 %q 的环境", name)
}

// FindHost 按地址返回机器指针, 不存在则返回 nil。返回的是切片元素指针, 可直接改。
func (e *Environment) FindHost(addr string) *Host {
	for i := range e.Hosts {
		if e.Hosts[i].Host == addr {
			return &e.Hosts[i]
		}
	}
	return nil
}

func (e *Environment) hasHost(addr string) bool {
	return e.FindHost(addr) != nil
}

// AddHost 往环境里加一台机器。地址重复则报错。
func (c *Config) AddHost(envName string, h Host) error {
	e := c.FindEnv(envName)
	if e == nil {
		return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", envName)
	}
	if err := ValidateHost(&h); err != nil {
		return err
	}
	if e.hasHost(h.Host) {
		return fmt.Errorf("环境 %q 已有机器 %s", envName, h.Host)
	}
	e.Hosts = append(e.Hosts, h)
	return nil
}

// RenameHost 改一台机器的地址, 原地修改不挪位置, 以免表格顺序跳动。
func (c *Config) RenameHost(envName, oldAddr, newAddr string) error {
	e := c.FindEnv(envName)
	if e == nil {
		return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", envName)
	}
	h := e.FindHost(oldAddr)
	if h == nil {
		return fmt.Errorf("环境 %q 里没有机器 %s (imgm host ls -e %s 查看)", envName, oldAddr, envName)
	}
	if oldAddr == newAddr {
		return nil
	}
	if e.hasHost(newAddr) {
		return fmt.Errorf("环境 %q 已有机器 %s", envName, newAddr)
	}
	probe := Host{Host: newAddr}
	if err := ValidateHost(&probe); err != nil {
		return err
	}
	h.Host = newAddr
	// 跳板机换了地址, 引用也得跟着走, 否则下次 Resolve 会说跳板机不在列表里。
	if e.Jump == oldAddr {
		e.Jump = newAddr
	}
	return nil
}

// RemoveHost 从环境里删一台机器。删掉还有别人依赖的跳板机会让整个环境连不上,
// 所以要求先显式取消跳板机设置。
func (c *Config) RemoveHost(envName, addr string) error {
	e := c.FindEnv(envName)
	if e == nil {
		return fmt.Errorf("不存在名为 %q 的环境 (imgm env ls 查看已有环境)", envName)
	}
	if e.Jump == addr && len(e.Hosts) > 1 {
		return fmt.Errorf("%s 是环境 %q 的跳板机, 其余 %d 台机器要靠它中转; 先执行 imgm env set %s --jump \"\" 或换一台跳板机",
			addr, envName, len(e.Hosts)-1, envName)
	}
	for i := range e.Hosts {
		if e.Hosts[i].Host == addr {
			e.Hosts = append(e.Hosts[:i], e.Hosts[i+1:]...)
			if e.Jump == addr {
				e.Jump = ""
			}
			return nil
		}
	}
	return fmt.Errorf("环境 %q 里没有机器 %s (imgm host ls -e %s 查看)", envName, addr, envName)
}
