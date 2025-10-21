package integrity

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"strings"
)

// StreamingHasher calculates multiple hashes simultaneously during streaming
type StreamingHasher struct {
	md5Hash    hash.Hash
	sha1Hash   hash.Hash
	sha256Hash hash.Hash
	crc32Hash  hash.Hash32
	size       int64
}

// StreamingHashes contains all calculated hashes
type StreamingHashes struct {
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	CRC32  string `json:"crc32"`
	Size   int64  `json:"size"`
}

// IntegrityResult contains verification results
type IntegrityResult struct {
	SourceETag      string `json:"source_etag"`
	DestETag        string `json:"dest_etag"`
	CalculatedMD5   string `json:"calculated_md5"`
	CalculatedSHA1  string `json:"calculated_sha1"`
	CalculatedSHA256 string `json:"calculated_sha256"`
	CalculatedCRC32 string `json:"calculated_crc32"`
	SourceSize      int64  `json:"source_size"`
	DestSize        int64  `json:"dest_size"`
	ETagMatch       bool   `json:"etag_match"`
	SizeMatch       bool   `json:"size_match"`
	MD5Match        bool   `json:"md5_match"`
	SHA1Match       bool   `json:"sha1_match"`
	IsValid         bool   `json:"is_valid"`
	Provider        string `json:"provider"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

// NewStreamingHasher creates a new streaming hasher
func NewStreamingHasher() *StreamingHasher {
	return &StreamingHasher{
		md5Hash:    md5.New(),
		sha1Hash:   sha1.New(),
		sha256Hash: sha256.New(),
		crc32Hash:  crc32.NewIEEE(),
		size:       0,
	}
}

// Write implements io.Writer interface
// Calculates all hashes simultaneously as data flows through
func (sh *StreamingHasher) Write(p []byte) (n int, err error) {
	// Write to all hash calculators simultaneously
	n, err = io.MultiWriter(sh.md5Hash, sh.sha1Hash, sh.sha256Hash, sh.crc32Hash).Write(p)
	if err != nil {
		return n, err
	}
	sh.size += int64(n)
	return n, nil
}

// GetHashes returns all calculated hashes
func (sh *StreamingHasher) GetHashes() *StreamingHashes {
	return &StreamingHashes{
		MD5:    hex.EncodeToString(sh.md5Hash.Sum(nil)),
		SHA1:   hex.EncodeToString(sh.sha1Hash.Sum(nil)),
		SHA256: hex.EncodeToString(sh.sha256Hash.Sum(nil)),
		CRC32:  fmt.Sprintf("%08x", sh.crc32Hash.Sum32()),
		Size:   sh.size,
	}
}

// ProviderType represents different S3-compatible storage providers
type ProviderType string

const (
	ProviderAWS         ProviderType = "aws"
	ProviderMinIO       ProviderType = "minio"
	ProviderWasabi      ProviderType = "wasabi"
	ProviderBackblazeB2 ProviderType = "backblaze-b2"
	ProviderCloudflareR2 ProviderType = "cloudflare-r2"
	ProviderDOSpaces    ProviderType = "do-spaces"
	ProviderGeneric     ProviderType = "generic-s3"
)

// DetectProvider detects the S3-compatible storage provider from endpoint
func DetectProvider(endpoint string) ProviderType {
	endpoint = strings.ToLower(endpoint)
	
	switch {
	case strings.Contains(endpoint, "amazonaws.com"):
		return ProviderAWS
	case strings.Contains(endpoint, "minio"):
		return ProviderMinIO
	case strings.Contains(endpoint, "wasabisys.com"):
		return ProviderWasabi
	case strings.Contains(endpoint, "backblazeb2.com") || strings.Contains(endpoint, "b2api.com"):
		return ProviderBackblazeB2
	case strings.Contains(endpoint, "r2.cloudflarestorage.com"):
		return ProviderCloudflareR2
	case strings.Contains(endpoint, "digitaloceanspaces.com"):
		return ProviderDOSpaces
	default:
		return ProviderGeneric
	}
}

// CleanETag removes quotes and extra characters from ETag
func CleanETag(etag string) string {
	etag = strings.Trim(etag, "\"")
	etag = strings.TrimSpace(etag)
	return etag
}

// IsMultipartETag checks if ETag is from a multipart upload
func IsMultipartETag(etag string) bool {
	// Multipart ETags contain a dash (e.g., "abc123-5")
	cleanETag := CleanETag(etag)
	return strings.Contains(cleanETag, "-")
}

// VerifyIntegrity verifies integrity based on provider type
func VerifyIntegrity(etag string, hashes *StreamingHashes, provider ProviderType) (bool, string) {
	cleanETag := CleanETag(etag)
	
	// Check if multipart upload
	if IsMultipartETag(cleanETag) {
		// For multipart uploads, we can't verify hash directly
		// Just verify ETag exists
		return cleanETag != "", "multipart"
	}
	
	// Single part upload - verify hash based on provider
	switch provider {
	case ProviderAWS, ProviderMinIO, ProviderWasabi, ProviderCloudflareR2, ProviderDOSpaces:
		// MD5-based providers
		match := hashes.MD5 == cleanETag
		if match {
			return true, "md5"
		}
		return false, "md5"
	
	case ProviderBackblazeB2:
		// SHA1-based provider
		match := hashes.SHA1 == cleanETag
		if match {
			return true, "sha1"
		}
		return false, "sha1"
	
	default:
		// Generic provider - just verify ETag exists
		return cleanETag != "", "etag"
	}
}

// CreateIntegrityResult creates a comprehensive integrity result
func CreateIntegrityResult(
	sourceETag, destETag string,
	hashes *StreamingHashes,
	sourceSize int64,
	sourceProvider, destProvider ProviderType,
) *IntegrityResult {
	result := &IntegrityResult{
		SourceETag:       sourceETag,
		DestETag:         destETag,
		CalculatedMD5:    hashes.MD5,
		CalculatedSHA1:   hashes.SHA1,
		CalculatedSHA256: hashes.SHA256,
		CalculatedCRC32:  hashes.CRC32,
		SourceSize:       sourceSize,
		DestSize:         hashes.Size,
		Provider:         string(destProvider),
	}
	
	// Verify ETags match
	result.ETagMatch = CleanETag(sourceETag) == CleanETag(destETag)
	
	// Verify size matches
	result.SizeMatch = sourceSize == hashes.Size
	
	// Verify hash based on provider
	sourceMatch, sourceHashType := VerifyIntegrity(sourceETag, hashes, sourceProvider)
	destMatch, destHashType := VerifyIntegrity(destETag, hashes, destProvider)
	
	// Set hash match flags
	if sourceHashType == "md5" || destHashType == "md5" {
		result.MD5Match = sourceMatch && destMatch
	}
	if sourceHashType == "sha1" || destHashType == "sha1" {
		result.SHA1Match = sourceMatch && destMatch
	}
	
	// Overall validity
	result.IsValid = result.ETagMatch && result.SizeMatch
	
	// Add error message if invalid
	if !result.IsValid {
		var errors []string
		if !result.ETagMatch {
			errors = append(errors, "ETag mismatch")
		}
		if !result.SizeMatch {
			errors = append(errors, fmt.Sprintf("Size mismatch: source=%d, dest=%d", sourceSize, hashes.Size))
		}
		result.ErrorMessage = strings.Join(errors, "; ")
	}
	
	return result
}

// CalculateMultipartETag calculates expected ETag for multipart upload
func CalculateMultipartETag(partHashes []string) string {
	// Concatenate all part hashes
	var concatenated string
	for _, hash := range partHashes {
		concatenated += hash
	}
	
	// Calculate MD5 of concatenated hashes
	finalHash := md5.Sum([]byte(concatenated))
	
	// Format as "hash-partcount"
	return fmt.Sprintf("%s-%d", hex.EncodeToString(finalHash[:]), len(partHashes))
}

