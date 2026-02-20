package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

// LanguageHandler handles language listing requests.
type LanguageHandler struct{}

// NewLanguageHandler creates a new LanguageHandler.
func NewLanguageHandler() *LanguageHandler {
	return &LanguageHandler{}
}

// List handles GET /api/v1/languages
func (h *LanguageHandler) List(c *gin.Context) {
	languages := []domain.LanguageInfo{
		{
			Name:    domain.LangPython,
			Version: "3.12",
		},
		{
			Name:     domain.LangCpp,
			Version:  "17",
			Compiler: "g++ (GCC 13)",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"languages": languages,
	})
}
