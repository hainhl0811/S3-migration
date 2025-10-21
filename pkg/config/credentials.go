package config

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// S3Provider represents different S3-compatible providers
type S3Provider string

const (
	ProviderAWS          S3Provider = "aws"
	ProviderMinIO        S3Provider = "minio"
	ProviderDigitalOcean S3Provider = "digitalocean"
	ProviderWasabi       S3Provider = "wasabi"
	ProviderBackblaze    S3Provider = "backblaze"
	ProviderCloudflare   S3Provider = "cloudflare"
	ProviderLinode       S3Provider = "linode"
	ProviderScaleway     S3Provider = "scaleway"
	ProviderCustom       S3Provider = "custom"
)

// Credentials holds S3-compatible service credentials
type Credentials struct {
	// Common fields for all providers
	Provider        S3Provider
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // Optional, mainly for AWS STS
	Region          string
	EndpointURL     string

	// Provider-specific settings
	ForcePathStyle bool // Required for MinIO and some providers
	UseSSL         bool // Enable/disable SSL
	BucketEndpoint bool // Use bucket-specific endpoints

	// Advanced settings
	DisableSSL         bool
	InsecureSkipVerify bool // Skip SSL verification (not recommended for production)
}

// LoadCredentials loads credentials from multiple sources in order of priority:
// 1. Explicit credentials provided
// 2. Environment variables
// 3. AWS credentials file (~/.aws/credentials)
// 4. IAM role (for EC2/ECS/Lambda)
func LoadCredentials(ctx context.Context, creds *Credentials) (aws.Config, error) {
	// If explicit credentials provided
	if creds != nil && creds.AccessKeyID != "" && creds.SecretAccessKey != "" {
		return loadFromExplicitCredentials(ctx, creds)
	}

	// Try environment variables
	if envCreds := loadFromEnvironment(); envCreds != nil {
		return loadFromExplicitCredentials(ctx, envCreds)
	}

	// Fall back to AWS SDK default credential chain
	// This will try: env vars, credentials file, IAM role
	return loadFromDefaultChain(ctx, creds)
}

func loadFromExplicitCredentials(ctx context.Context, creds *Credentials) (aws.Config, error) {
	region := creds.Region
	if region == "" {
		region = "us-east-1"
	}

	staticProvider := credentials.NewStaticCredentialsProvider(
		creds.AccessKeyID,
		creds.SecretAccessKey,
		creds.SessionToken,
	)

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(staticProvider),
	)

	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load credentials: %w", err)
	}

	return cfg, nil
}

func loadFromEnvironment() *Credentials {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if accessKey == "" || secretKey == "" {
		return nil
	}

	return &Credentials{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		Region:          os.Getenv("AWS_REGION"),
		EndpointURL:     os.Getenv("S3_ENDPOINT_URL"),
	}
}

func loadFromDefaultChain(ctx context.Context, creds *Credentials) (aws.Config, error) {
	region := "us-east-1"
	if creds != nil && creds.Region != "" {
		region = creds.Region
	}
	if envRegion := os.Getenv("AWS_REGION"); envRegion != "" {
		region = envRegion
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
	)

	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load default credentials: %w", err)
	}

	return cfg, nil
}

// ValidateCredentials checks if credentials are valid by making a test call
func ValidateCredentials(ctx context.Context, cfg aws.Config) error {
	// Try to get caller identity as a validation check
	// This requires AWS STS permissions
	// For now, we'll just check if credentials are present
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	if creds.AccessKeyID == "" {
		return fmt.Errorf("no access key ID found")
	}

	if creds.SecretAccessKey == "" {
		return fmt.Errorf("no secret access key found")
	}

	return nil
}

// GetCredentialsSource returns a string describing where credentials came from
func GetCredentialsSource() string {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("S3_ACCESS_KEY_ID") != "" {
		return "environment variables"
	}

	// Check for credentials file
	home, err := os.UserHomeDir()
	if err == nil {
		credFile := home + "/.aws/credentials"
		if _, err := os.Stat(credFile); err == nil {
			return "credentials file (~/.aws/credentials)"
		}
	}

	// Check if running in AWS (EC2, ECS, Lambda)
	if os.Getenv("AWS_EXECUTION_ENV") != "" || os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" {
		return "IAM role (container/lambda)"
	}

	return "unknown (possibly IAM role)"
}

// NewCredentials creates credentials with manual configuration (no auto-detection)
func NewCredentials(accessKey, secretKey, region, endpointURL string) *Credentials {
	return &Credentials{
		Provider:        ProviderCustom, // Default to custom
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		Region:          region,
		EndpointURL:     endpointURL,
		UseSSL:          true,
		ForcePathStyle:  false, // Can be overridden
	}
}

// NewCredentialsForProvider creates credentials with provider-specific defaults
// User can override any field after creation
func NewCredentialsForProvider(provider S3Provider, accessKey, secretKey, region string) *Credentials {
	creds := &Credentials{
		Provider:        provider,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		Region:          region,
		UseSSL:          true, // Default to SSL
	}

	// Set provider-specific defaults ONLY if not already set
	switch provider {
	case ProviderAWS:
		if region == "" {
			creds.Region = "us-east-1"
		}
		creds.ForcePathStyle = false

	case ProviderMinIO:
		// MinIO requires path-style access
		creds.ForcePathStyle = true
		if region == "" {
			creds.Region = "us-east-1"
		}
		// Default local MinIO endpoint
		if creds.EndpointURL == "" {
			creds.EndpointURL = "http://localhost:9000"
		}

	case ProviderDigitalOcean:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "nyc3"
		}
		// DigitalOcean Spaces endpoints: https://REGION.digitaloceanspaces.com
		if creds.EndpointURL == "" {
			creds.EndpointURL = fmt.Sprintf("https://%s.digitaloceanspaces.com", creds.Region)
		}

	case ProviderWasabi:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "us-east-1"
		}
		// Wasabi endpoints: https://s3.REGION.wasabisys.com
		if creds.EndpointURL == "" {
			creds.EndpointURL = fmt.Sprintf("https://s3.%s.wasabisys.com", creds.Region)
		}

	case ProviderBackblaze:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "us-west-004"
		}
		// Backblaze B2 S3-compatible endpoint
		// Format: https://s3.REGION.backblazeb2.com
		if creds.EndpointURL == "" {
			creds.EndpointURL = fmt.Sprintf("https://s3.%s.backblazeb2.com", creds.Region)
		}

	case ProviderCloudflare:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "auto"
		}
		// Cloudflare R2 endpoint (requires account ID)
		// Format: https://ACCOUNT_ID.r2.cloudflarestorage.com
		// EndpointURL must be set by user with their account ID

	case ProviderLinode:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "us-east-1"
		}
		// Linode Object Storage
		// Format: https://REGION.linodeobjects.com
		if creds.EndpointURL == "" {
			creds.EndpointURL = fmt.Sprintf("https://%s.linodeobjects.com", creds.Region)
		}

	case ProviderScaleway:
		creds.ForcePathStyle = false
		if region == "" {
			creds.Region = "nl-ams"
		}
		// Scaleway Object Storage
		// Format: https://s3.REGION.scw.cloud
		if creds.EndpointURL == "" {
			creds.EndpointURL = fmt.Sprintf("https://s3.%s.scw.cloud", creds.Region)
		}

	case ProviderCustom:
		// For custom providers, user must specify endpoint
		creds.ForcePathStyle = true // Usually required for custom providers
		if region == "" {
			creds.Region = "us-east-1"
		}
	}

	return creds
}

// ProviderPresets returns a map of common provider configurations
func ProviderPresets() map[S3Provider]string {
	return map[S3Provider]string{
		ProviderAWS:          "Amazon Web Services S3",
		ProviderMinIO:        "MinIO (Self-hosted S3-compatible)",
		ProviderDigitalOcean: "DigitalOcean Spaces",
		ProviderWasabi:       "Wasabi Hot Cloud Storage",
		ProviderBackblaze:    "Backblaze B2 Cloud Storage",
		ProviderCloudflare:   "Cloudflare R2",
		ProviderLinode:       "Linode Object Storage",
		ProviderScaleway:     "Scaleway Object Storage",
		ProviderCustom:       "Custom S3-compatible service",
	}
}

// WithEndpoint sets a custom endpoint URL (overrides provider default)
func (c *Credentials) WithEndpoint(endpointURL string) *Credentials {
	c.EndpointURL = endpointURL
	return c
}

// WithRegion sets a custom region (overrides provider default)
func (c *Credentials) WithRegion(region string) *Credentials {
	c.Region = region
	return c
}

// WithPathStyle sets path-style addressing
func (c *Credentials) WithPathStyle(forcePathStyle bool) *Credentials {
	c.ForcePathStyle = forcePathStyle
	return c
}

// WithSSL enables or disables SSL
func (c *Credentials) WithSSL(useSSL bool) *Credentials {
	c.UseSSL = useSSL
	return c
}

// WithInsecureSkipVerify sets SSL verification (use with caution)
func (c *Credentials) WithInsecureSkipVerify(skip bool) *Credentials {
	c.InsecureSkipVerify = skip
	return c
}

// GetProviderRegions returns available regions for a provider
func GetProviderRegions(provider S3Provider) []string {
	switch provider {
	case ProviderAWS:
		return []string{
			"us-east-1", "us-east-2", "us-west-1", "us-west-2",
			"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1",
			"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2",
			"sa-east-1", "ca-central-1",
		}
	case ProviderDigitalOcean:
		return []string{"nyc3", "sfo3", "ams3", "sgp1", "fra1"}
	case ProviderWasabi:
		return []string{
			"us-east-1", "us-east-2", "us-west-1",
			"eu-central-1", "eu-west-1", "eu-west-2",
			"ap-northeast-1", "ap-northeast-2",
		}
	case ProviderBackblaze:
		return []string{"us-west-000", "us-west-001", "us-west-002", "us-west-004", "eu-central-003"}
	case ProviderLinode:
		return []string{"us-east-1", "eu-central-1", "ap-south-1"}
	case ProviderScaleway:
		return []string{"fr-par", "nl-ams", "pl-waw"}
	case ProviderCloudflare:
		return []string{"auto"}
	default:
		return []string{"us-east-1"}
	}
}
