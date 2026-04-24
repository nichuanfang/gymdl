package cron

import (
	"net/http"
	"sync"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
)

// installDependency 安装依赖项
func installDependency(c *config.Config, client *http.Client) {
	group := sync.WaitGroup{}
	group.Add(3)
	go func() {
		defer group.Done()
		installPipDependency()
	}()
	go func() {
		defer group.Done()
		installUm(client)
	}()
	go func() {
		defer group.Done()
		syncCookieCloud()
	}()
	group.Wait()
}

// updateDependency 更新依赖项
func updateDependency(c *config.Config, client *http.Client) {
	group := sync.WaitGroup{}
	group.Add(1)
	go func() {
		defer group.Done()
		updatePipDependency()
	}()
    // 由于网站不可访问 暂时关闭um更新
	// go func() {
	// 	defer group.Done()
	// 	updateUm(client)
	// }()
	group.Wait()
}

// syncCookieCloud 同步cookie
func syncCookieCloud() {
	core.GlobalCookieCloud.Sync()
}
