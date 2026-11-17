package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/cron"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

func HandleCronJobs(c *gin.Context) {
	enabled, jobs := cron.Jobs()
	response.Success(c, gin.H{"enabled": enabled, "jobs": jobs})
}

func HandleRunCronJob(c *gin.Context) {
	name := c.Param("name")
	if err := cron.RunNow(name); err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, gin.H{"name": name, "started": true})
}
