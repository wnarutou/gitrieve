package server

import (
	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
)

type API struct {
	config *config.Config
	db     *db.DB
}

func NewAPI(cfg *config.Config, db *db.DB) *API {
	return &API{config: cfg, db: db}
}

func (a *API) GetJobs(c *gin.Context) {
	jobs := make([]Job, 0)

	// Query jobs from config
	for _, repo := range a.config.Repository {
		jobs = append(jobs, Job{
			Name:   repo.Name,
			URL:    repo.URL,
			Status: "idle",
		})
	}

	c.JSON(200, Response{
		Code: 200,
		Data: jobs,
	})
}