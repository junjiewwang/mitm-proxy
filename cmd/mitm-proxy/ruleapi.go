package main

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mitm-proxy/rule"
)

// registerRuleAPI 注册规则管理 REST API
func registerRuleAPI(engine *gin.Engine, manager *rule.RuleManager) {
	api := engine.Group("/api/rules")
	{
		api.GET("", listRules(manager))
		api.GET("/:id", getRule(manager))
		api.POST("", createRule(manager))
		api.PUT("/:id", updateRule(manager))
		api.DELETE("/:id", deleteRule(manager))

		api.POST("/:id/enable", enableRule(manager))
		api.POST("/:id/disable", disableRule(manager))
		api.POST("/:id/reload", reloadRule(manager))

		api.POST("/reload-all", reloadAllRules(manager))
		api.POST("/validate", validateRules(manager))
	}
}

func listRules(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		scripts := m.List()
		type scriptInfo struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Priority   int    `json:"priority"`
			Enabled    bool   `json:"enabled"`
			RuleCount  int    `json:"rule_count"`
			Intercepts int    `json:"intercept_count"`
			TotalHits  int64  `json:"total_hits"`
			HasError   bool   `json:"has_error"`
		}
		result := make([]scriptInfo, 0, len(scripts))
		for _, s := range scripts {
			result = append(result, scriptInfo{
				ID:         s.ID,
				Name:       s.Name,
				Priority:   s.Priority,
				Enabled:    s.Enabled,
				RuleCount:  len(s.Rules),
				Intercepts: len(s.Intercepts),
				TotalHits:  s.Stats.TotalHits,
				HasError:   len(s.ParseErrors) > 0,
			})
		}
		c.JSON(http.StatusOK, gin.H{"scripts": result})
	}
}

func getRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		script, ok := m.Get(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "script not found"})
			return
		}
		c.JSON(http.StatusOK, script)
	}
}

func createRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Name    string `json:"name" binding:"required"`
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		script, err := m.Create(req.Name, req.Content)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, script)
	}
}

func updateRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := m.Update(id, req.Content); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
	}
}

func deleteRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := m.Delete(id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	}
}

func enableRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := m.Enable(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "enabled"})
	}
}

func disableRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := m.Disable(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "disabled"})
	}
}

func reloadRule(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := m.Reload(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "reloaded"})
	}
}

func reloadAllRules(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := m.ReloadAll(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "reloaded"})
	}
}

func validateRules(m *rule.RuleManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		errors := m.Validate(req.Content)
		c.JSON(http.StatusOK, gin.H{
			"valid":  len(errors) == 0,
			"errors": errors,
		})
	}
}
