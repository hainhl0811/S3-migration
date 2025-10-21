package googledrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// OAuthConfig holds OAuth configuration
type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
}

// TokenResponse represents the response from OAuth token exchange
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// AuthHandler handles Google Drive OAuth authentication
type AuthHandler struct {
	config *oauth2.Config
	ctx    context.Context
}

// NewAuthHandler creates a new Google Drive auth handler
func NewAuthHandler(ctx context.Context, oauthConfig OAuthConfig) *AuthHandler {
	config := &oauth2.Config{
		ClientID:     oauthConfig.ClientID,
		ClientSecret: oauthConfig.ClientSecret,
		RedirectURL:  oauthConfig.RedirectURL,
		Scopes: []string{
			"https://www.googleapis.com/auth/drive.readonly",
		},
		Endpoint: google.Endpoint,
	}

	return &AuthHandler{
		config: config,
		ctx:    ctx,
	}
}

// GetAuthURL generates the OAuth authorization URL
func (h *AuthHandler) GetAuthURL(state string) string {
	return h.config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// ExchangeCodeForToken exchanges an authorization code for an access token
func (h *AuthHandler) ExchangeCodeForToken(code string) (*TokenResponse, error) {
	token, err := h.config.Exchange(h.ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code for token: %w", err)
	}

	return &TokenResponse{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresIn:    int64(token.Expiry.Sub(token.Expiry).Seconds()),
		Scope:        token.Extra("scope").(string),
	}, nil
}

// RefreshToken refreshes an expired access token
func (h *AuthHandler) RefreshToken(refreshToken string) (*TokenResponse, error) {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}

	newToken, err := h.config.TokenSource(h.ctx, token).Token()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	return &TokenResponse{
		AccessToken:  newToken.AccessToken,
		RefreshToken: newToken.RefreshToken,
		TokenType:    newToken.TokenType,
		ExpiresIn:    int64(newToken.Expiry.Sub(newToken.Expiry).Seconds()),
		Scope:        newToken.Extra("scope").(string),
	}, nil
}

// ValidateToken validates if a token is still valid
func (h *AuthHandler) ValidateToken(accessToken string) error {
	// Create a test client with the token
	client := h.config.Client(h.ctx, &oauth2.Token{AccessToken: accessToken})
	
	// Make a simple API call to validate the token
	resp, err := client.Get("https://www.googleapis.com/drive/v3/about?fields=user")
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token validation failed with status: %d", resp.StatusCode)
	}

	return nil
}

// LoadCredentialsFromFile loads OAuth credentials from a JSON file
func LoadCredentialsFromFile(filename string) (*OAuthConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var config OAuthConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse credentials file: %w", err)
	}

	return &config, nil
}

// SaveCredentialsToFile saves OAuth credentials to a JSON file
func SaveCredentialsToFile(config *OAuthConfig, filename string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(filename, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}
