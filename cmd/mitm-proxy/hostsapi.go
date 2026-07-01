package main

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// registerHostsAPI 注册 hosts 文件管理 API
func registerHostsAPI(engine *gin.Engine, hostsPath string, reloadFn func() error) {
	api := engine.Group("/api/hosts")
	api.GET("", getHosts(hostsPath))
	api.PUT("", updateHosts(hostsPath, reloadFn))
}

func getHosts(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		content, err := os.ReadFile(path)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read hosts file: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"content": string(content)})
	}
}

func updateHosts(path string, reloadFn func() error) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := os.WriteFile(path, []byte(req.Content), 0644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write hosts file: " + err.Error()})
			return
		}
		if reloadFn != nil {
			if err := reloadFn(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "reload hosts: " + err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
	}
}
