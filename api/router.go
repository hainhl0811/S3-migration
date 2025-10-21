package api

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// SetupRouter creates and configures the Gin router
func SetupRouter() *gin.Engine {
	router := gin.Default()
	
	// Initialize scheduler on startup
	EnsureSchedulerInitialized()
	
	// Serve static files and web UI
	router.Static("/static", "./web/static")
	router.StaticFile("/", "./web/index.html")
	router.StaticFile("/auth/callback", "./web/auth-callback.html")
	router.NoRoute(func(c *gin.Context) {
		c.File("./web/index.html")
	})

	// Configure CORS
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"*"} // Configure appropriately in production
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	router.Use(cors.New(config))

	// Health check
	router.GET("/health", HealthCheck)

	// API routes
	api := router.Group("/api")
	{
		// Debug endpoints
		api.POST("/test-connection", TestConnection)
		api.POST("/test-bucket-listing", TestBucketListing)
		api.GET("/debug/task/:taskID/errors", GetTaskErrors)
		
		// One-time migrations
		api.POST("/migrate", StartMigration)
		api.POST("/migrate/bulk", StartBulkMigration) // Migrate all buckets
		api.GET("/status/:taskID", GetStatus)
		api.GET("/tasks", ListTasks)
		api.DELETE("/tasks/:taskID", CancelTask)
		api.DELETE("/tasks/cleanup/:status", CleanupTasks) // Delete tasks by status (failed, completed, cancelled)
		// Retry removed: credentials not persisted for security
		// api.POST("/tasks/:taskID/retry", RetryTask)

		// Scheduled migrations
		api.POST("/schedules", CreateSchedule)
		api.GET("/schedules", ListSchedules)
		api.GET("/schedules/stats", GetSchedulerStats)
		api.GET("/schedules/:id", GetSchedule)
		api.PUT("/schedules/:id", UpdateSchedule)
		api.DELETE("/schedules/:id", DeleteSchedule)
		api.POST("/schedules/:id/enable", EnableSchedule)
		api.POST("/schedules/:id/disable", DisableSchedule)
		api.POST("/schedules/:id/run", RunScheduleNow)

                // Google Drive integration
                api.POST("/googledrive/quick-auth-url", GoogleDriveQuickAuthURL)
                api.POST("/googledrive/auth-url", GoogleDriveAuthURL)
                api.POST("/googledrive/exchange-token", GoogleDriveExchangeToken)
                api.POST("/googledrive/list-folders", GoogleDriveListFolders)
                api.POST("/googledrive/migrate", StartGoogleDriveMigration)
	}

	return router
}
