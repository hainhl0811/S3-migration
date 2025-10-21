package googledrive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Client wraps the Google Drive API client
type Client struct {
	service     *drive.Service
	ctx         context.Context
	oauthConfig *oauth2.Config
	token       *oauth2.Token
}

// FileInfo represents a Google Drive file
type FileInfo struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	MimeType     string    `json:"mime_type"`
	ModifiedTime time.Time `json:"modified_time"`
	Parents      []string  `json:"parents"`
	IsFolder     bool      `json:"is_folder"`
}

// Config holds Google Drive client configuration
type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// NewClient creates a new Google Drive client
func NewClient(ctx context.Context, config Config) (*Client, error) {
	// If ClientID/ClientSecret are empty, use the public OAuth app credentials
	clientID := config.ClientID
	clientSecret := config.ClientSecret
	if clientID == "" {
		// Use OAuth app credentials from environment for token refresh
		clientID = os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			return nil, fmt.Errorf("Google OAuth not configured: GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET environment variables must be set")
		}
	}

	// Create OAuth2 config
	oauthConfig := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  config.RedirectURL,
		Scopes: []string{
			drive.DriveReadonlyScope, // Read-only access to Google Drive
		},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}

	// Create token from access and refresh tokens
	// Let OAuth2 client handle token refresh automatically when needed
	token := &oauth2.Token{
		AccessToken:  config.AccessToken,
		RefreshToken: config.RefreshToken,
		// Don't set Expiry - let the OAuth2 client determine when to refresh
		// This prevents excessive refresh attempts
	}

	fmt.Printf("🔐 Creating Google Drive client with automatic token refresh\n")
	fmt.Printf("   Using ClientID: %s...\n", clientID[:20])
	fmt.Printf("   Tokens will auto-refresh when expired\n")

	// Create optimized HTTP client for high throughput (750 GB/day target)
	// Supports 50 concurrent workers for maximum download speed
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        200,               // Increased for 50 workers
			MaxIdleConnsPerHost: 100,               // Increased to support 50 concurrent downloads
			IdleConnTimeout:     90 * time.Second,  // Keep connections alive longer
			TLSHandshakeTimeout: 10 * time.Second,  // Faster TLS handshake
			DisableCompression:  false,             // Enable compression for efficiency
		},
		Timeout: 30 * time.Second, // Reasonable timeout for API calls
	}
	
	// Create context with optimized HTTP client
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	
	// Create HTTP client with token - this will automatically refresh tokens
	client := oauthConfig.Client(tokenCtx, token)

	// Create Drive service
	service, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Drive service: %w", err)
	}

	return &Client{
		service:     service,
		ctx:         ctx,
		oauthConfig: oauthConfig,
		token:       token,
	}, nil
}

// refreshToken manually refreshes the OAuth token
func (c *Client) refreshToken() error {
	if c.oauthConfig == nil || c.token == nil {
		return fmt.Errorf("oauth config or token not available")
	}

	// Create a new token source
	tokenSource := c.oauthConfig.TokenSource(c.ctx, c.token)
	
	// Get a fresh token
	newToken, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	// Check if the new token is valid (has a non-zero expiry)
	if newToken.Expiry.IsZero() {
		return fmt.Errorf("refresh token has expired or is invalid - please re-authenticate using Quick Login")
	}

	// Update the stored token
	c.token = newToken
	
	// Token refreshed successfully (removed verbose logging for better UX)
	return nil
}

// ListFiles lists files in a Google Drive folder
func (c *Client) ListFiles(folderID string, pageSize int64) ([]FileInfo, string, error) {
	return c.ListFilesWithToken(folderID, pageSize, "")
}

// ListFilesWithToken lists files in a Google Drive folder with pagination support
func (c *Client) ListFilesWithToken(folderID string, pageSize int64, pageToken string) ([]FileInfo, string, error) {
	return c.ListFilesWithTokenAndOptions(folderID, pageSize, pageToken, false)
}

// ListFilesWithTokenAndOptions lists files with control over shared files
func (c *Client) ListFilesWithTokenAndOptions(folderID string, pageSize int64, pageToken string, includeShared bool) ([]FileInfo, string, error) {
	// Build query
	var query string
	if includeShared {
		// Include all files (owned + shared with me)
		query = "trashed=false"
	} else {
		// Only include files owned by user (not shared files)
		query = "trashed=false and 'me' in owners"
	}
	
	if folderID != "" {
		query += fmt.Sprintf(" and '%s' in parents", folderID)
	}

	// Create list call
	call := c.service.Files.List().
		Q(query).
		Fields("nextPageToken, files(id, name, size, mimeType, modifiedTime, parents)").
		PageSize(pageSize)

	// Add page token if provided
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}

	// Execute the call with retry logic for auth errors
	var result *drive.FileList
	var err error
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = call.Do()
		if err == nil {
			break // Success!
		}

		// Check if it's an auth error (token expired)
		if attempt < maxRetries && (strings.Contains(err.Error(), "401") || 
			strings.Contains(err.Error(), "Invalid Credentials") ||
			strings.Contains(err.Error(), "authError")) {
			
			// Try to refresh the token manually (silent retry for better UX)
			if refreshErr := c.refreshToken(); refreshErr != nil {
				// If refresh token is expired, don't retry - fail immediately with clear message
				if strings.Contains(refreshErr.Error(), "please re-authenticate") {
					fmt.Printf("❌ Authentication expired: %v\n", refreshErr)
					return nil, "", fmt.Errorf("authentication expired - %w", refreshErr)
				}
				// Continue with retry for other refresh errors
			}
			
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		// Non-auth error or max retries reached
		return nil, "", fmt.Errorf("failed to list files: %w", err)
	}

	// Convert to FileInfo
	var files []FileInfo
	for _, file := range result.Files {
		fileInfo := FileInfo{
			ID:       file.Id,
			Name:     file.Name,
			MimeType: file.MimeType,
			Parents:  file.Parents,
		}

		// Set size (Google Drive API returns size as int64)
		fileInfo.Size = file.Size

		// Parse modified time
		if file.ModifiedTime != "" {
			if modifiedTime, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
				fileInfo.ModifiedTime = modifiedTime
			}
		}

		// Check if it's a folder
		fileInfo.IsFolder = file.MimeType == "application/vnd.google-apps.folder"

		files = append(files, fileInfo)
	}

	return files, result.NextPageToken, nil
}

// GetFile downloads a file from Google Drive or exports Google Workspace files
func (c *Client) GetFile(fileID string) (io.ReadCloser, error) {
	// First, get file metadata to check mime type with retry logic
	var file *drive.File
	var err error
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		file, err = c.service.Files.Get(fileID).Fields("id, mimeType").Do()
		if err == nil {
			break // Success!
		}

		// Check if it's an auth error (token expired)
		if attempt < maxRetries && (strings.Contains(err.Error(), "401") || 
			strings.Contains(err.Error(), "Invalid Credentials") ||
			strings.Contains(err.Error(), "authError")) {
			
			// Try to refresh the token manually (silent retry for better UX)
			if refreshErr := c.refreshToken(); refreshErr != nil {
				// If refresh token is expired, don't retry - fail immediately with clear message
				if strings.Contains(refreshErr.Error(), "please re-authenticate") {
					return nil, fmt.Errorf("authentication expired - %w", refreshErr)
				}
			}
			
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		// Non-auth error or max retries reached
		return nil, fmt.Errorf("failed to get file metadata: %w", err)
	}

	// Check if it's a Google Workspace file that needs to be exported
	exportMimeType := c.getExportMimeType(file.MimeType)
	if exportMimeType != "" {
		// Export Google Workspace file with retry logic
		var resp *http.Response
		maxRetries := 3
		for attempt := 1; attempt <= maxRetries; attempt++ {
			resp, err = c.service.Files.Export(fileID, exportMimeType).Download()
			if err == nil {
				break // Success!
			}

			// Check if it's an auth error (token expired)
			if attempt < maxRetries && (strings.Contains(err.Error(), "401") || 
				strings.Contains(err.Error(), "Invalid Credentials") ||
				strings.Contains(err.Error(), "authError")) {
				
				// Try to refresh the token manually (silent retry for better UX)
				if refreshErr := c.refreshToken(); refreshErr != nil {
					// If refresh token is expired, don't retry - fail immediately with clear message
					if strings.Contains(refreshErr.Error(), "please re-authenticate") {
						return nil, fmt.Errorf("authentication expired - %w", refreshErr)
					}
				}
				
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}

			// Non-auth error or max retries reached
			return nil, fmt.Errorf("failed to export file: %w", err)
		}
		return resp.Body, nil
	}

	// Regular file - download directly with retry logic
	var resp *http.Response
	maxRetries = 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err = c.service.Files.Get(fileID).Download()
		if err == nil {
			break // Success!
		}

		// Check if it's an auth error (token expired)
		if attempt < maxRetries && (strings.Contains(err.Error(), "401") || 
			strings.Contains(err.Error(), "Invalid Credentials") ||
			strings.Contains(err.Error(), "authError")) {
			
			// Try to refresh the token manually (silent retry for better UX)
			if refreshErr := c.refreshToken(); refreshErr != nil {
				// If refresh token is expired, don't retry - fail immediately with clear message
				if strings.Contains(refreshErr.Error(), "please re-authenticate") {
					return nil, fmt.Errorf("authentication expired - %w", refreshErr)
				}
			}
			
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		// Non-auth error or max retries reached
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	return resp.Body, nil
}

// getExportMimeType returns the export mime type for Google Workspace files
func (c *Client) getExportMimeType(mimeType string) string {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document" // .docx
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" // .xlsx
	case "application/vnd.google-apps.presentation":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation" // .pptx
	case "application/vnd.google-apps.drawing":
		return "application/pdf" // .pdf
	case "application/vnd.google-apps.script":
		return "application/vnd.google-apps.script+json" // .json
	default:
		return "" // Not a Google Workspace file, download normally
	}
}

// GetFileInfo gets metadata for a specific file
func (c *Client) GetFileInfo(fileID string) (*FileInfo, error) {
	// Get file info with retry logic
	var file *drive.File
	var err error
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		file, err = c.service.Files.Get(fileID).
			Fields("id, name, size, mimeType, modifiedTime, parents").
			Do()
		if err == nil {
			break // Success!
		}

		// Check if it's an auth error (token expired)
		if attempt < maxRetries && (strings.Contains(err.Error(), "401") || 
			strings.Contains(err.Error(), "Invalid Credentials") ||
			strings.Contains(err.Error(), "authError")) {
			
			// Try to refresh the token manually (silent retry for better UX)
			if refreshErr := c.refreshToken(); refreshErr != nil {
				// If refresh token is expired, don't retry - fail immediately with clear message
				if strings.Contains(refreshErr.Error(), "please re-authenticate") {
					return nil, fmt.Errorf("authentication expired - %w", refreshErr)
				}
			}
			
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		// Non-auth error or max retries reached
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	fileInfo := &FileInfo{
		ID:       file.Id,
		Name:     file.Name,
		MimeType: file.MimeType,
		Parents:  file.Parents,
	}

		// Set size (Google Drive API returns size as int64)
		fileInfo.Size = file.Size

	// Parse modified time
	if file.ModifiedTime != "" {
		if modifiedTime, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
			fileInfo.ModifiedTime = modifiedTime
		}
	}

	// Check if it's a folder
	fileInfo.IsFolder = file.MimeType == "application/vnd.google-apps.folder"

	return fileInfo, nil
}

// ListFolders lists folders in Google Drive
func (c *Client) ListFolders(parentFolderID string) ([]FileInfo, error) {
	// Only list folders owned by the user
	query := "trashed=false and 'me' in owners and mimeType='application/vnd.google-apps.folder'"
	if parentFolderID != "" {
		query += fmt.Sprintf(" and '%s' in parents", parentFolderID)
	}

	call := c.service.Files.List().
		Q(query).
		Fields("files(id, name, mimeType, modifiedTime, parents)").
		PageSize(1000) // Folders are usually fewer

	result, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list folders: %w", err)
	}

	var folders []FileInfo
	for _, file := range result.Files {
		folderInfo := FileInfo{
			ID:       file.Id,
			Name:     file.Name,
			MimeType: file.MimeType,
			Parents:  file.Parents,
			IsFolder: true,
		}

		// Parse modified time
		if file.ModifiedTime != "" {
			if modifiedTime, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
				folderInfo.ModifiedTime = modifiedTime
			}
		}

		folders = append(folders, folderInfo)
	}

	return folders, nil
}

// parseFileSize parses file size from string to int64
func parseFileSize(sizeStr string) (int64, error) {
	// Google Drive API returns size as string
	// This is a simple implementation - in production you might want to handle edge cases
	var size int64
	_, err := fmt.Sscanf(sizeStr, "%d", &size)
	return size, err
}
