package main

import (
	"fmt"
	"log"
	"os"

	"s3migration/api"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// Initialize task manager with RDS database backend
	dbDriver := os.Getenv("DB_DRIVER")
	if dbDriver == "" {
		dbDriver = "postgres" // Default to PostgreSQL
	}
	
	dbConnectionString := os.Getenv("DB_CONNECTION_STRING")
	if dbConnectionString == "" {
		log.Fatal("DB_CONNECTION_STRING environment variable is required")
	}
	
	fmt.Printf("Initializing task manager with %s database...\n", dbDriver)
	if err := api.InitTaskManager(dbDriver, dbConnectionString); err != nil {
		log.Fatal("Failed to initialize task manager:", err)
	}

	router := api.SetupRouter()

	fmt.Printf("Starting S3 Migration API server on port %s...\n", port)
	fmt.Printf("API Documentation: http://localhost:%s/health\n", port)
	fmt.Printf("Health Check: http://localhost:%s/health\n", port)

	if err := router.Run(":" + port); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
