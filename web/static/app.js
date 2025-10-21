// S3 Migration Tool - Frontend JavaScript

const API_BASE = '';

// Utility function to format bytes
function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

// Auto-refresh control
let autoRefreshInterval = null;

function startAutoRefresh() {
    // Clear any existing interval
    if (autoRefreshInterval) {
        clearInterval(autoRefreshInterval);
    }
    
    // Auto-refresh every 15 seconds when on tasks tab (reduced frequency for better UX)
    autoRefreshInterval = setInterval(() => {
        const tasksTab = document.getElementById('tasks-tab');
        if (tasksTab && tasksTab.classList.contains('active')) {
            refreshTasks();
        }
    }, 15000); // 15 seconds for better UX
}

function stopAutoRefresh() {
    if (autoRefreshInterval) {
        clearInterval(autoRefreshInterval);
        autoRefreshInterval = null;
    }
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', async () => {
    // Load dark mode preference
    const darkMode = localStorage.getItem('darkMode') === 'true';
    if (darkMode) {
        document.body.classList.add('dark-mode');
        document.querySelector('.theme-icon').textContent = '☀️';
    }
    
    // Health check removed - no health status element in simplified UI
    
    // Setup form handlers
    const migrationForm = document.getElementById('migrationForm');
    if (migrationForm) {
        migrationForm.addEventListener('submit', handleMigrationSubmit);
    }
    
    const scheduleForm = document.getElementById('scheduleForm');
    if (scheduleForm) {
        scheduleForm.addEventListener('submit', handleScheduleSubmit);
    }
    
    // Wait a bit before loading data to ensure server is ready
    setTimeout(() => {
        refreshTasks();
        refreshSchedules();
        startAutoRefresh(); // Start auto-refresh
    }, 500);
});

// Toggle Dark Mode
function toggleDarkMode() {
    const body = document.body;
    const icon = document.querySelector('.theme-icon');
    
    body.classList.toggle('dark-mode');
    const isDark = body.classList.contains('dark-mode');
    
    // Update icon
    icon.textContent = isDark ? '☀️' : '🌙';
    
    // Save preference
    localStorage.setItem('darkMode', isDark);
}

// S3 Provider configurations
const S3_PROVIDERS = {
    aws: {
        name: 'AWS S3',
        endpoint: '',
        requiresRegion: true,
        defaultRegion: 'us-east-1'
    },
    cmc: {
        name: 'CMC Telecom S3',
        endpoint: 'https://s3.hn-1.cloud.cmctelecom.vn',
        requiresRegion: false,
        defaultRegion: ''
    },
    minio: {
        name: 'MinIO',
        endpoint: '',
        requiresRegion: false,
        defaultRegion: ''
    },
    wasabi: {
        name: 'Wasabi',
        endpoint: 'https://s3.wasabisys.com',
        requiresRegion: true,
        defaultRegion: 'us-east-1'
    },
    backblaze: {
        name: 'Backblaze B2',
        endpoint: 'https://s3.us-west-000.backblazeb2.com',
        requiresRegion: false,
        defaultRegion: ''
    },
    googledrive: {
        name: 'Google Drive',
        endpoint: '',
        requiresRegion: false,
        defaultRegion: '',
        isGoogleDrive: true
    },
    custom: {
        name: 'Custom S3-Compatible',
        endpoint: '',
        requiresRegion: false,
        defaultRegion: ''
    }
};

function updateSourceProvider() {
    const provider = document.getElementById('sourceProvider').value;
    console.log('🔄 updateSourceProvider called with provider:', provider);
    const config = S3_PROVIDERS[provider];
    
    const endpointInput = document.getElementById('sourceEndpoint');
    const regionSelect = document.getElementById('sourceRegionSelect');
    const regionInput = document.getElementById('sourceRegion');
    const endpointRow = document.getElementById('sourceEndpointRow');
    const regionGroup = document.getElementById('sourceRegionGroup');
    
    // Google Drive specific elements
    const s3Credentials = document.getElementById('sourceS3Credentials');
    const googleDriveCredentials = document.getElementById('sourceGoogleDriveCredentials');
    const bucketLabel = document.getElementById('sourceBucketLabel');
    const prefixLabel = document.getElementById('sourcePrefixLabel');
    
    if (provider === 'googledrive') {
        console.log('🎯 Hiding S3 credentials for Google Drive');
        // Hide S3-specific elements
        s3Credentials.style.display = 'none';
        endpointRow.style.display = 'none';
        regionGroup.style.display = 'none';
        
        // Remove required attributes from S3 fields to prevent validation errors
        document.getElementById('sourceAccessKey').removeAttribute('required');
        document.getElementById('sourceSecretKey').removeAttribute('required');
        
        // Show Google Drive elements
        googleDriveCredentials.style.display = 'block';
        
        // Update labels for Google Drive
        bucketLabel.textContent = 'Folder (leave empty for ALL files)';
        prefixLabel.textContent = 'File filter (optional)';
        
        document.getElementById('sourceBucket').placeholder = 'Leave empty for all files, or specify folder ID';
        document.getElementById('sourcePrefix').placeholder = 'file-pattern or empty for all files';
        
        // Auto-set redirect URL based on current domain
        const redirectURLInput = document.getElementById('sourceRedirectURL');
        if (!redirectURLInput.value) {
            const currentDomain = window.location.origin;
            redirectURLInput.value = currentDomain + '/auth/callback';
            redirectURLInput.placeholder = currentDomain + '/auth/callback';
        }
        
    } else {
        // Hide Google Drive elements
        googleDriveCredentials.style.display = 'none';
        
        // Show S3 elements
        s3Credentials.style.display = 'flex';
        regionGroup.style.display = 'flex';
        endpointRow.style.display = 'flex';
        
        // Restore required attributes for S3 fields
        document.getElementById('sourceAccessKey').setAttribute('required', 'required');
        document.getElementById('sourceSecretKey').setAttribute('required', 'required');
        
        // Restore S3 labels
        bucketLabel.textContent = 'Bucket (leave empty to migrate ALL buckets)';
        prefixLabel.textContent = 'Prefix (filter within bucket)';
        
        document.getElementById('sourceBucket').placeholder = 'Leave empty for all buckets, or specify: my-bucket';
        document.getElementById('sourcePrefix').placeholder = 'folder/ or empty for entire bucket';
        
        // Handle S3 provider configuration
        if (config.requiresRegion) {
            // AWS or Wasabi - show region dropdown
            regionSelect.style.display = 'block';
            regionInput.style.display = 'none';
            regionSelect.value = config.defaultRegion;
            if (provider === 'aws') {
                endpointRow.style.display = 'none';
                endpointInput.value = '';
            } else {
                endpointRow.style.display = 'flex';
                endpointInput.value = config.endpoint;
            }
        } else {
            // S3-compatible - show text input for region (optional)
            regionSelect.style.display = 'none';
            regionInput.style.display = 'block';
            regionInput.value = config.defaultRegion;
            endpointRow.style.display = 'flex';
            endpointInput.value = config.endpoint;
        }
    }
}

function updateDestProvider() {
    const provider = document.getElementById('destProvider').value;
    const credentialsRow = document.getElementById('destCredentialsRow');
    const endpointRow = document.getElementById('destEndpointRow');
    const regionSelect = document.getElementById('destRegionSelect');
    const regionInput = document.getElementById('destRegion');
    const endpointInput = document.getElementById('destEndpoint');
    
    if (provider === 'same') {
        // Show credentials even when using same provider (user can override)
        credentialsRow.style.display = 'flex';
        endpointRow.style.display = 'none';
        regionSelect.style.display = 'none';
        regionInput.style.display = 'none';
    } else {
        // Show credentials for different provider
        credentialsRow.style.display = 'flex';
        
        const config = S3_PROVIDERS[provider];
        
        if (config.requiresRegion) {
            regionSelect.style.display = 'block';
            regionInput.style.display = 'none';
            regionSelect.value = config.defaultRegion;
            if (provider === 'aws') {
                endpointRow.style.display = 'none';
                endpointInput.value = '';
            } else {
                endpointRow.style.display = 'flex';
                endpointInput.value = config.endpoint;
            }
        } else {
            regionSelect.style.display = 'none';
            regionInput.style.display = 'block';
            regionInput.value = config.defaultRegion;
            endpointRow.style.display = 'flex';
            endpointInput.value = config.endpoint;
        }
    }
}

// Update schedule visibility based on migration mode
function updateScheduleVisibility() {
    const mode = document.querySelector('input[name="migrationMode"]:checked').value;
    const scheduleOptions = document.getElementById('scheduleOptions');
    
    if (mode === 'incremental') {
        scheduleOptions.style.display = 'block';
    } else {
        scheduleOptions.style.display = 'none';
    }
}

// Update schedule fields based on schedule type
function updateScheduleFields() {
    const scheduleType = document.querySelector('input[name="scheduleType"]:checked')?.value;
    const onceScheduleFields = document.getElementById('onceScheduleFields');
    const recurringScheduleFields = document.getElementById('recurringScheduleFields');
    
    // Hide all schedule fields by default
    if (onceScheduleFields) onceScheduleFields.style.display = 'none';
    if (recurringScheduleFields) recurringScheduleFields.style.display = 'none';
    
    // Show appropriate fields based on selection
    if (scheduleType === 'once') {
        if (onceScheduleFields) onceScheduleFields.style.display = 'block';
    } else if (scheduleType === 'recurring') {
        if (recurringScheduleFields) recurringScheduleFields.style.display = 'block';
    }
    // 'now' type doesn't need any additional fields
}

// Update cron fields based on frequency
function updateCronFields() {
    const frequency = document.getElementById('scheduleFrequency').value;
    const cronFields = document.getElementById('cronFields');
    const timeFields = document.getElementById('timeFields');
    
    if (frequency === 'custom') {
        cronFields.style.display = 'block';
        timeFields.style.display = 'none';
    } else {
        cronFields.style.display = 'none';
        timeFields.style.display = 'block';
    }
}

// Handle scheduled incremental sync
async function handleScheduledMigration(event, scheduleType) {
    let scheduleName, cronExpression;
    
    if (scheduleType === 'once') {
        // One-time schedule
        scheduleName = document.getElementById('onceScheduleName').value.trim() || 'One-time Migration';
        const scheduleDate = document.getElementById('onceScheduleDate').value;
        const scheduleTime = document.getElementById('onceScheduleTime').value || '02:00';
        
        if (!scheduleDate) {
            alert('⚠️ Please select a date for the scheduled migration.');
            return;
        }
        
        // Convert date/time to cron expression (one-time)
        const datetime = new Date(`${scheduleDate}T${scheduleTime}`);
        const minute = datetime.getMinutes();
        const hour = datetime.getHours();
        const day = datetime.getDate();
        const month = datetime.getMonth() + 1;
        
        // One-time cron: specific minute, hour, day, month, any weekday
        cronExpression = `${minute} ${hour} ${day} ${month} *`;
        
    } else if (scheduleType === 'recurring') {
        // Recurring schedule
        scheduleName = document.getElementById('recurringScheduleName').value.trim() || 'Recurring Migration';
        const frequency = document.getElementById('scheduleFrequency').value;
        
        if (frequency === 'custom') {
            cronExpression = document.getElementById('cronExpression').value.trim();
        } else {
            const time = document.getElementById('scheduleTime').value || '02:00';
            const [hour, minute] = time.split(':');
            
            switch (frequency) {
                case 'hourly':
                    cronExpression = `${minute} * * * *`;
                    break;
                case 'daily':
                    cronExpression = `${minute} ${hour} * * *`;
                    break;
                case 'weekly':
                    cronExpression = `${minute} ${hour} * * 0`;
                    break;
            }
        }
    }
    
    const scheduleData = {
        name: scheduleName,
        source_bucket: document.getElementById('sourceBucket').value.trim(),
        dest_bucket: document.getElementById('destBucket').value.trim(),
        source_prefix: document.getElementById('sourcePrefix').value.trim(),
        dest_prefix: document.getElementById('destPrefix').value.trim(),
        cron: cronExpression,
        enabled: true,
        migration_mode: 'incremental',  // Scheduled migrations use incremental mode
        run_once: scheduleType === 'once',  // Flag for one-time execution
    };
    
    // Add credentials if provided
    if (document.getElementById('sourceAccessKey').value) {
        scheduleData.source_credentials = {
            access_key: document.getElementById('sourceAccessKey').value,
            secret_key: document.getElementById('sourceSecretKey').value,
            region: document.getElementById('sourceRegion').value || 'us-east-1',
            endpoint_url: document.getElementById('sourceEndpoint').value
        };
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/schedules`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(scheduleData)
        });
        
        const result = await response.json();
        
        if (response.ok) {
            alert(`✅ Schedule Created!\n\nName: ${scheduleName}\nCron: ${cronExpression}\n\nThe incremental sync will run automatically.`);
            
            // Switch to schedules tab
            showTab('schedules');
            refreshSchedules();
            
            // Reset form
            document.getElementById('migrationForm').reset();
        } else {
            alert(`❌ Error: ${result.error || 'Failed to create schedule'}`);
        }
    } catch (error) {
        alert(`❌ Error: ${error.message}`);
    }
}

// Health Check
async function checkHealth() {
    try {
        const response = await fetch(`${API_BASE}/health`);
        const data = await response.json();
        
        const statusEl = document.getElementById('healthStatus');
        if (statusEl) {
            const indicator = statusEl.querySelector('.status-indicator');
            const text = statusEl.querySelector('.status-text');
            
            if (indicator && text) {
                if (response.ok) {
                    indicator.classList.add('healthy');
                    text.textContent = 'Connected';
                } else {
                    indicator.classList.remove('healthy');
                    text.textContent = 'Error';
                }
            }
        }
    } catch (error) {
        const statusEl = document.getElementById('healthStatus');
        if (statusEl) {
            const indicator = statusEl.querySelector('.status-indicator');
            const text = statusEl.querySelector('.status-text');
            
            if (indicator && text) {
                indicator.classList.remove('healthy');
                text.textContent = 'Disconnected';
            }
        }
    }
}

// Test Connection
async function testConnection(event) {
    const button = event.target;
    const originalText = button.textContent;
    
    try {
        button.textContent = 'Testing...';
        button.disabled = true;
        
        // Get credentials from form (using source credentials for test)
        const credentials = {
            access_key: document.getElementById('sourceAccessKey').value.trim(),
            secret_key: document.getElementById('sourceSecretKey').value.trim(),
            region: document.getElementById('sourceRegion').value.trim() || 'us-east-1',
            endpoint_url: document.getElementById('sourceEndpoint').value.trim()
        };
        
        const response = await fetch(`${API_BASE}/api/test-connection`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(credentials)
        });
        
        const data = await response.json();
        
        if (response.ok) {
            alert('✅ Connection successful!\n\nRegion: ' + data.region + 
                  (data.endpoint ? '\nEndpoint: ' + data.endpoint : '') +
                  '\nCredentials: ' + data.credentials_source);
            console.log('Connection details:', data);
        } else {
            alert('❌ Connection failed: ' + data.message + 
                  (data.hint ? '\n\nHint: ' + data.hint : ''));
            console.error('Connection error:', data);
        }
        
    } catch (error) {
        alert('❌ Connection test failed: ' + error.message);
        console.error('Connection test error:', error);
    } finally {
        button.textContent = originalText;
        button.disabled = false;
    }
}

// Tab Management
function showTab(tabName) {
    // Hide all tabs
    document.querySelectorAll('.tab-content').forEach(tab => {
        tab.classList.remove('active');
    });
    
    document.querySelectorAll('.tab').forEach(btn => {
        btn.classList.remove('active');
    });
    
    // Show selected tab
    document.getElementById(`${tabName}-tab`).classList.add('active');
    event.target.classList.add('active');
    
    // Only refresh on initial tab load, not every switch
    // Auto-refresh will handle updates for tasks tab
    if (tabName === 'schedules') {
        const schedulesList = document.getElementById('schedulesList');
        // Only refresh if empty or showing loading
        if (!schedulesList || schedulesList.innerHTML.includes('Loading') || schedulesList.innerHTML.includes('No schedules')) {
            refreshSchedules();
        }
    }
}

// Provider Endpoint Mapping
const providerEndpoints = {
    aws: '',
    minio: 'http://localhost:9000',
    digitalocean: (region) => `https://${region}.digitaloceanspaces.com`,
    wasabi: (region) => `https://s3.${region}.wasabisys.com`,
    backblaze: (region) => `https://s3.${region}.backblazeb2.com`,
    cloudflare: '', // Requires account ID
    custom: ''
};

function updateEndpoint(type) {
    const provider = document.getElementById(`${type}Provider`).value;
    const region = document.getElementById(`${type}Region`).value || 'us-east-1';
    const endpointInput = document.getElementById(`${type}Endpoint`);
    
    if (typeof providerEndpoints[provider] === 'function') {
        endpointInput.value = providerEndpoints[provider](region);
    } else {
        endpointInput.value = providerEndpoints[provider];
    }
}

// Toggle bulk migration mode for schedules
function toggleScheduleBulkMode() {
    const bulkChecked = document.getElementById('schedMigrateAllBuckets').checked;
    const bulkOptions = document.getElementById('schedBulkOptions');
    const sourceBucket = document.getElementById('schedSourceBucket');
    const destBucket = document.getElementById('schedDestBucket');
    const sourceBucketRequired = document.getElementById('schedSourceBucketRequired');
    const destBucketRequired = document.getElementById('schedDestBucketRequired');
    
    if (bulkChecked) {
        bulkOptions.classList.remove('hidden');
        sourceBucket.disabled = true;
        destBucket.disabled = true;
        sourceBucket.required = false;
        destBucket.required = false;
        sourceBucket.value = '';
        destBucket.value = '';
        sourceBucket.placeholder = '(All buckets will be synced)';
        destBucket.placeholder = '(Buckets created with same names)';
        sourceBucketRequired.style.display = 'none';
        destBucketRequired.style.display = 'none';
    } else {
        bulkOptions.classList.add('hidden');
        sourceBucket.disabled = false;
        destBucket.disabled = false;
        sourceBucket.required = true;
        destBucket.required = true;
        sourceBucket.placeholder = 'my-source-bucket';
        destBucket.placeholder = 'my-dest-bucket';
        sourceBucketRequired.style.display = 'inline';
        destBucketRequired.style.display = 'inline';
    }
}

// Toggle bulk migration mode
function toggleBulkMode() {
    const bulkChecked = document.getElementById('migrateAllBuckets').checked;
    const bulkOptions = document.getElementById('bulkOptions');
    const sourceBucket = document.getElementById('sourceBucket');
    const destBucket = document.getElementById('destBucket');
    const sourceBucketRequired = document.getElementById('sourceBucketRequired');
    const destBucketRequired = document.getElementById('destBucketRequired');
    
    if (bulkChecked) {
        bulkOptions.classList.remove('hidden');
        sourceBucket.disabled = true;
        destBucket.disabled = true;
        sourceBucket.required = false;
        destBucket.required = false;
        sourceBucket.value = '';
        destBucket.value = '';
        sourceBucket.placeholder = '(All buckets will be migrated)';
        destBucket.placeholder = '(Buckets created with same names)';
        sourceBucketRequired.style.display = 'none';
        destBucketRequired.style.display = 'none';
    } else {
        bulkOptions.classList.add('hidden');
        sourceBucket.disabled = false;
        destBucket.disabled = false;
        sourceBucket.required = true;
        destBucket.required = true;
        sourceBucket.placeholder = 'my-source-bucket';
        destBucket.placeholder = 'my-dest-bucket';
        sourceBucketRequired.style.display = 'inline';
        destBucketRequired.style.display = 'inline';
    }
}

// Migration Form Handler
async function handleMigrationSubmit(event) {
    console.log('🚀 handleMigrationSubmit called!', event);
    event.preventDefault();
    
    // Get migration mode
    const modeRadio = document.querySelector('input[name="migrationMode"]:checked');
    const mode = modeRadio ? modeRadio.value : 'full_rewrite';
    console.log('📋 Migration mode:', mode);
    
    // Check if it's a scheduled incremental sync
    const isIncremental = mode === 'incremental';
    const scheduleTypeRadio = document.querySelector('input[name="scheduleType"]:checked');
    const scheduleType = scheduleTypeRadio ? scheduleTypeRadio.value : 'now';
    
    // If it's a scheduled migration (one-time or recurring), create a schedule instead of running now
    if (isIncremental && (scheduleType === 'once' || scheduleType === 'recurring')) {
        return handleScheduledMigration(event, scheduleType);
    }
    
    // WARNING: Confirm full rewrite mode
    if (mode === 'full_rewrite' && !document.getElementById('dryRun').checked) {
        const sourceBucketPreview = document.getElementById('sourceBucket').value.trim() || 'ALL BUCKETS';
        const destBucketPreview = document.getElementById('destBucket').value.trim() || 'matching source names';
        
        const confirmed = confirm(
            '⚠️ FULL REWRITE MODE - DESTRUCTIVE OPERATION ⚠️\n\n' +
            'This will OVERWRITE ALL FILES in the destination!\n\n' +
            'Source: ' + sourceBucketPreview + '\n' +
            'Destination: ' + destBucketPreview + '\n\n' +
            '❌ All existing files in destination will be replaced\n' +
            '❌ This action cannot be undone\n\n' +
            '💡 Consider using:\n' +
            '   • Incremental mode for safer syncing\n' +
            '   • Dry Run first to preview changes\n\n' +
            'Are you absolutely sure you want to proceed?'
        );
        
        if (!confirmed) {
            return; // User cancelled
        }
    }
    
    const sourceProvider = document.getElementById('sourceProvider').value;
    
    // Handle Google Drive migrations differently
    if (sourceProvider === 'googledrive') {
        return handleGoogleDriveMigration(event, mode);
    }
    
    const sourceBucket = document.getElementById('sourceBucket').value.trim();
    const destBucket = document.getElementById('destBucket').value.trim();
    
    // Validate: if source bucket is empty, dest bucket must also be empty (all buckets migration)
    if (!sourceBucket && destBucket) {
        alert('⚠️ When migrating ALL buckets (source bucket empty), destination bucket must also be empty.\n\nThe tool will create matching bucket names in the destination.');
        return;
    }
    
    // Validate: if source bucket is specified, dest bucket is required
    if (sourceBucket && !destBucket) {
        alert('⚠️ Destination bucket is required when source bucket is specified.\n\nLeave both empty to migrate all buckets, or specify both.');
        return;
    }
    
    const timeoutEl = document.getElementById('timeout');
    const migrationData = {
        source_bucket: sourceBucket,
        dest_bucket: destBucket,
        source_prefix: document.getElementById('sourcePrefix').value.trim(),
        dest_prefix: document.getElementById('destPrefix').value.trim(),
        dry_run: document.getElementById('dryRun').checked,
        migration_mode: mode,  // Use new migration_mode field: "full_rewrite" or "incremental"
        timeout: timeoutEl ? parseInt(timeoutEl.value || 3600) : 3600
    };
    
    // Source credentials (REQUIRED)
    const sourceAccessKey = document.getElementById('sourceAccessKey').value.trim();
    const sourceSecretKey = document.getElementById('sourceSecretKey').value.trim();
    
    // Get region from select or input depending on provider
    let sourceRegion = '';
    if (S3_PROVIDERS[sourceProvider].requiresRegion) {
        sourceRegion = document.getElementById('sourceRegionSelect').value;
    } else {
        sourceRegion = document.getElementById('sourceRegion').value.trim();
    }
    
    const sourceEndpoint = document.getElementById('sourceEndpoint').value.trim();
    
    if (!sourceAccessKey || !sourceSecretKey) {
        alert('❌ Source credentials are required!\n\nPlease provide Access Key and Secret Key for the source bucket.');
        return;
    }
    
    migrationData.source_credentials = {
        access_key: sourceAccessKey,
        secret_key: sourceSecretKey,
        region: sourceRegion,
        endpoint_url: sourceEndpoint
    };
    
    // Destination credentials (optional - defaults to source if not provided)
    const destProvider = document.getElementById('destProvider').value;
    const destAccessKey = document.getElementById('destAccessKey').value.trim();
    const destSecretKey = document.getElementById('destSecretKey').value.trim();
    
    // Get dest region from select or input depending on provider
    let destRegion = '';
    if (destProvider === 'same') {
        destRegion = sourceRegion;
    } else if (S3_PROVIDERS[destProvider].requiresRegion) {
        destRegion = document.getElementById('destRegionSelect').value;
    } else {
        destRegion = document.getElementById('destRegion').value.trim();
    }
    
    const destEndpoint = document.getElementById('destEndpoint').value.trim() || sourceEndpoint;
    
    if (destAccessKey && destSecretKey) {
        migrationData.dest_credentials = {
            access_key: destAccessKey,
            secret_key: destSecretKey,
            region: destRegion,
            endpoint_url: destEndpoint
        };
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/migrate`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(migrationData)
        });
        
        const result = await response.json();
        
        if (response.ok) {
            showResult('migrationResult', 'success', `
                <h3>✅ Migration Started!</h3>
                <div class="result-info">
                    <div><span>Task ID:</span><code>${result.task_id}</code></div>
                    <div><span>Status:</span><span>${result.status}</span></div>
                </div>
                <p style="margin-top: 15px;">
                    <a href="#" onclick="showTab('tasks'); return false;">View in Active Tasks</a>
                </p>
            `);
            
            // Auto-switch to tasks tab
            setTimeout(() => {
                showTab('tasks');
                document.querySelector('[onclick*="tasks"]').click();
            }, 2000);
        } else {
            showResult('migrationResult', 'error', `
                <h3>❌ Error</h3>
                <p>${result.error || 'Failed to start migration'}</p>
            `);
        }
    } catch (error) {
        showResult('migrationResult', 'error', `
            <h3>❌ Error</h3>
            <p>Failed to connect to API: ${error.message}</p>
        `);
    }
}

// Bulk Migration Handler
async function handleBulkMigrationSubmit() {
    if (!confirm('⚠️ This will migrate ALL buckets in your source account. Continue?')) {
        return;
    }
    
    const bulkData = {
        source_region: document.getElementById('sourceRegion').value || 'us-east-1',
        source_endpoint: document.getElementById('sourceEndpoint').value,
        dest_region: document.getElementById('destRegion').value || 'us-east-1',
        dest_endpoint: document.getElementById('destEndpoint').value,
        dry_run: document.getElementById('dryRun').checked,
        timeout: document.getElementById('timeout') ? parseInt(document.getElementById('timeout').value || 3600) : 3600,
        concurrent: parseInt(document.getElementById('concurrent').value) || 3
    };
    
    // Parse exclude buckets
    const excludeInput = document.getElementById('excludeBuckets').value;
    if (excludeInput.trim()) {
        bulkData.exclude_buckets = excludeInput.split(',').map(b => b.trim()).filter(b => b);
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/migrate/bulk`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(bulkData)
        });
        
        const result = await response.json();
        
        if (response.ok) {
            showResult('migrationResult', 'success', `
                <h3>✅ Bulk Migration Started!</h3>
                <p>${result.message}</p>
                <div class="result-info">
                    <div><span>Task ID:</span><code>${result.task_id}</code></div>
                    <div><span>Status:</span><span>${result.status}</span></div>
                </div>
                <p style="margin-top: 15px;">
                    This will migrate all buckets. Check server logs for progress.
                </p>
            `);
        } else {
            showResult('migrationResult', 'error', `
                <h3>❌ Error</h3>
                <p>${result.error || 'Failed to start bulk migration'}</p>
            `);
        }
    } catch (error) {
        showResult('migrationResult', 'error', `
            <h3>❌ Error</h3>
            <p>Failed to connect to API: ${error.message}</p>
        `);
    }
}

// Schedule Form Handler
async function handleScheduleSubmit(event) {
    event.preventDefault();
    
    const isBulk = document.getElementById('schedMigrateAllBuckets').checked;
    
    const scheduleData = {
        name: document.getElementById('scheduleName').value,
        cron_expr: document.getElementById('cronExpr').value,
        source_bucket: isBulk ? '*' : document.getElementById('schedSourceBucket').value,
        dest_bucket: isBulk ? '*' : document.getElementById('schedDestBucket').value,
        source_prefix: document.getElementById('schedSourcePrefix').value,
        dest_prefix: document.getElementById('schedDestPrefix').value,
        incremental: document.getElementById('incremental').checked,
        delete_removed: document.getElementById('deleteRemoved').checked,
        conflict_strategy: document.getElementById('conflictStrategy').value
    };
    
    // Add exclude buckets if bulk mode
    if (isBulk) {
        const excludeInput = document.getElementById('schedExcludeBuckets').value;
        if (excludeInput.trim()) {
            scheduleData.exclude_buckets = excludeInput.split(',').map(b => b.trim()).filter(b => b);
        }
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/schedules`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(scheduleData)
        });
        
        const result = await response.json();
        
        if (response.ok) {
            showResult('scheduleResult', 'success', `
                <h3>✅ Schedule Created!</h3>
                <div class="result-info">
                    <div><span>Schedule ID:</span><code>${result.id}</code></div>
                    <div><span>Name:</span><span>${result.name}</span></div>
                    <div><span>Cron:</span><code>${result.cron_expr}</code></div>
                    <div><span>Next Run:</span><span>${formatDate(result.next_run)}</span></div>
                </div>
            `);
            
            // Reset form and switch to schedules tab
            resetScheduleForm();
            setTimeout(() => {
                showTab('schedules');
                document.querySelector('[onclick*="schedules"]').click();
            }, 2000);
        } else {
            showResult('scheduleResult', 'error', `
                <h3>❌ Error</h3>
                <p>${result.error || 'Failed to create schedule'}</p>
            `);
        }
    } catch (error) {
        showResult('scheduleResult', 'error', `
            <h3>❌ Error</h3>
            <p>Failed to connect to API: ${error.message}</p>
        `);
    }
}


// Refresh Tasks
async function refreshTasks() {
    const tasksList = document.getElementById('tasksList');
    tasksList.innerHTML = '<p class="loading">Loading tasks...</p>';
    
    try {
        const response = await fetch(`${API_BASE}/api/tasks`);
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        
        const taskIds = await response.json();
        
        if (!taskIds || taskIds.length === 0) {
            tasksList.innerHTML = '<p class="text-muted">No active tasks</p>';
            return;
        }
        
        // Fetch details for each task
        const taskPromises = taskIds.map(async id => {
            try {
                const res = await fetch(`${API_BASE}/api/status/${id}`);
                if (!res.ok) return null;
                return await res.json();
            } catch (e) {
                return null;
            }
        });
        
        const tasks = (await Promise.all(taskPromises)).filter(t => t !== null);
        
        // Store all tasks for filtering
        allTasks = tasks;
        
        // Sort tasks consistently: running first, then by start time (newest first)
        tasks.sort((a, b) => {
            // Priority 1: Running/pending tasks first
            const statusOrder = { 'running': 0, 'pending': 1, 'completed': 2, 'failed': 3, 'cancelled': 4 };
            const aOrder = statusOrder[a.status] ?? 999;
            const bOrder = statusOrder[b.status] ?? 999;
            if (aOrder !== bOrder) return aOrder - bOrder;
            
            // Priority 2: Sort by start time (newest first)
            const aTime = new Date(a.start_time || 0).getTime();
            const bTime = new Date(b.start_time || 0).getTime();
            return bTime - aTime;
        });
        
        if (tasks.length === 0) {
            tasksList.innerHTML = '<p class="text-muted">No active tasks</p>';
        } else {
            // Apply current filter if any
            applyDateFilter();
        }
    } catch (error) {
        const errorMsg = error.message || 'Unknown error';
        tasksList.innerHTML = `
            <div class="result error">
                <h3>❌ Error Loading Tasks</h3>
                <p>${errorMsg}</p>
                <p style="margin-top: 10px;">
                    <strong>Troubleshooting:</strong>
                    <ul style="margin-top: 10px; margin-left: 20px;">
                        <li>Is the server running? Check <a href="/health" target="_blank">health endpoint</a></li>
                        <li>Check browser console (F12) for details</li>
                        <li>Verify API is accessible at <code>${API_BASE}/api/tasks</code></li>
                    </ul>
                </p>
                <button class="btn btn-primary" onclick="refreshTasks()">Retry</button>
            </div>
        `;
    }
}

function createTaskCard(task) {
    const progress = task.progress || 0;
    const statusClass = task.status.toLowerCase();
    
    // Add mode indicators
    const dryRunIndicator = task.dry_run ? '<span class="mode-badge dry-run">🔍 DRY RUN</span>' : '';
    const syncModeIndicator = task.sync_mode ? '<span class="mode-badge incremental">🔄 INCREMENTAL</span>' : '';
    
    // Add dry run verification details
    const dryRunDetails = task.dry_run && task.dry_run_verified ? `
        <div class="dry-run-section">
            <h4>🔍 Dry Run Verification</h4>
            <div class="verification-list">
                ${task.dry_run_verified.map(item => `<div class="verification-item">${item}</div>`).join('')}
            </div>
        </div>
    ` : '';
    
    // Add error details section for failed tasks
    const errorDetails = task.errors && task.errors.length > 0 ? `
        <div class="error-section">
            <h4>❌ Errors (${task.errors.length})</h4>
            <div class="error-list">
                ${task.errors.map(error => {
                    // Parse error message to make it more readable
                    let errorMsg = error;
                    let errorType = '⚠️';
                    let errorHint = '';
                    
                    if (error.includes('AccessDenied')) {
                        errorType = '🔒';
                        errorHint = 'Permission denied. Check your credentials and bucket permissions.';
                    } else if (error.includes('EntityTooLarge')) {
                        errorType = '📦';
                        errorHint = 'File too large for simple copy. Multipart upload required.';
                    } else if (error.includes('NoSuchBucket')) {
                        errorType = '🗂️';
                        errorHint = 'Bucket does not exist. Check bucket name.';
                    } else if (error.includes('InvalidAccessKeyId')) {
                        errorType = '🔑';
                        errorHint = 'Invalid access key. Verify your credentials.';
                    } else if (error.includes('SignatureDoesNotMatch')) {
                        errorType = '🔐';
                        errorHint = 'Invalid secret key. Verify your credentials.';
                    }
                    
                    return `
                        <div class="error-item">
                            <div class="error-icon">${errorType}</div>
                            <div class="error-content">
                                <div class="error-message">${errorMsg}</div>
                                ${errorHint ? `<div class="error-hint">💡 ${errorHint}</div>` : ''}
                            </div>
                        </div>
                    `;
                }).join('')}
            </div>
        </div>
    ` : '';
    
    return `
        <div class="task-card ${statusClass}">
            <div class="task-header">
                <span class="task-id">${task.task_id}</span>
                <div class="task-badges">
                    ${dryRunIndicator}
                    ${syncModeIndicator}
                </div>
                <span class="task-status ${statusClass}">${task.status}</span>
            </div>
            <div class="task-progress">
                <div class="progress-bar">
                    <div class="progress-fill" style="width: ${progress}%"></div>
                </div>
                <small>${progress.toFixed(1)}% complete</small>
            </div>
            <div class="task-details">
                <div class="detail-item">
                    <span class="detail-label">${task.migration_type === 'google-drive' ? 'Files' : 'Objects'}</span>
                    <span class="detail-value">${task.copied_objects || 0}/${task.total_objects || 0}</span>
                </div>
                <div class="detail-item">
                    <span class="detail-label">Capacity</span>
                    <span class="detail-value">${formatBytes(task.copied_size || 0)} / ${formatBytes(task.total_size || 0)}</span>
                </div>
                <div class="detail-item">
                    <span class="detail-label">Speed</span>
                    <span class="detail-value">${task.current_speed?.toFixed(1) || 0} MB/s</span>
                </div>
                <div class="detail-item">
                    <span class="detail-label">${(task.status === 'completed' || task.status === 'completed_with_errors') && task.duration ? 'Total Time' : 'ETA'}</span>
                    <span class="detail-value">${(task.status === 'completed' || task.status === 'completed_with_errors') && task.duration ? task.duration : (task.eta || 'calculating...')}</span>
                </div>
                <div class="detail-item">
                    <span class="detail-label">Started</span>
                    <span class="detail-value">${task.start_time ? formatDate(task.start_time) : 'Unknown'}</span>
                </div>
                ${task.end_time ? `
                <div class="detail-item">
                    <span class="detail-label">Completed</span>
                    <span class="detail-value">${formatDate(task.end_time)}</span>
                </div>
                ` : ''}
                ${task.status === 'running' ? `
                <div class="detail-item">
                    <span class="detail-label">Running For</span>
                    <span class="detail-value">${task.start_time ? getDuration(task.start_time) : 'Unknown'}</span>
                </div>
                ` : ''}
            </div>
            ${(task.eta === 'discovering...' || task.eta === 'starting upload...') ? `
                <div class="discovery-status">
                    <div class="discovery-indicator">
                        <div class="discovery-spinner"></div>
                        <span class="discovery-text">
                            ${task.eta === 'discovering...' ? '🔍 Discovering files in Google Drive...' : '🚀 Preparing to upload files...'}
                        </span>
                    </div>
                    <div class="discovery-note">
                        ${task.eta === 'discovering...' ? 'This may take a few minutes for large folders' : 'Upload will begin shortly'}
                    </div>
                </div>
            ` : ''}
            ${errorDetails}
            ${dryRunDetails}
            ${task.status === 'running' ? `
                <div class="task-actions">
                    <button class="btn btn-danger btn-small" onclick="cancelTask('${task.task_id}')">
                        Cancel
                    </button>
                </div>
            ` : task.status === 'failed' ? `
                <div class="task-actions">
                    <span class="task-failed-note">⚠️ To resume, start a new migration with the same source/destination. Already copied files will be skipped.</span>
                </div>
            ` : ''}
        </div>
    `;
}

// Refresh Schedules
async function refreshSchedules() {
    const schedulesList = document.getElementById('schedulesList');
    
    if (!schedulesList) {
        console.error('schedulesList element not found');
        return;
    }
    
    schedulesList.innerHTML = '<p class="loading">Loading schedules...</p>';
    
    try {
        const response = await fetch(`${API_BASE}/api/schedules`);
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        
        const schedules = await response.json();
        
        if (!schedules || schedules.length === 0) {
            schedulesList.innerHTML = '<p class="text-muted">No schedules configured</p>';
        } else {
            schedulesList.innerHTML = schedules.map(schedule => createScheduleCard(schedule)).join('');
        }
        
    } catch (error) {
        const errorMsg = error.message || 'Unknown error';
        schedulesList.innerHTML = `
            <div class="result error">
                <h3>❌ Error Loading Schedules</h3>
                <p>${errorMsg}</p>
                <p style="margin-top: 10px;">
                    <strong>Troubleshooting:</strong>
                    <ul style="margin-top: 10px; margin-left: 20px;">
                        <li>Is the server running? Check <a href="/health" target="_blank">health endpoint</a></li>
                        <li>Check browser console (F12) for details</li>
                        <li>Server may still be starting up</li>
                    </ul>
                </p>
                <button class="btn btn-primary" onclick="refreshSchedules()">Retry</button>
            </div>
        `;
    }
}

function createScheduleCard(schedule) {
    const disabledClass = schedule.enabled ? '' : 'disabled';
    
    return `
        <div class="schedule-card ${disabledClass}">
            <div class="task-header">
                <h3>${schedule.name}</h3>
                <span class="task-status ${schedule.enabled ? 'running' : 'paused'}">
                    ${schedule.enabled ? 'ENABLED' : 'DISABLED'}
                </span>
            </div>
            <div class="schedule-info">
                <p><strong>Schedule:</strong> <code>${schedule.cron_expr}</code></p>
                <p><strong>Source:</strong> ${schedule.source.bucket}/${schedule.source.prefix || ''}</p>
                <p><strong>Destination:</strong> ${schedule.destination.bucket}/${schedule.destination.prefix || ''}</p>
                <div class="schedule-meta">
                    <span>Runs: ${schedule.run_count || 0}</span>
                    <span>Failures: ${schedule.fail_count || 0}</span>
                    <span>Last: ${formatDate(schedule.last_run)}</span>
                    <span>Next: ${formatDate(schedule.next_run)}</span>
                </div>
                ${schedule.options?.incremental ? '<span class="text-success">✓ Incremental</span>' : ''}
                ${schedule.options?.delete_removed ? '<span class="text-warning">⚠️ Delete Removed</span>' : ''}
            </div>
            <div class="task-actions">
                <button class="btn btn-primary btn-small" onclick="runScheduleNow('${schedule.id}')">
                    Run Now
                </button>
                ${schedule.enabled ? `
                    <button class="btn btn-secondary btn-small" onclick="toggleSchedule('${schedule.id}', false)">
                        Disable
                    </button>
                ` : `
                    <button class="btn btn-success btn-small" onclick="toggleSchedule('${schedule.id}', true)">
                        Enable
                    </button>
                `}
                <button class="btn btn-danger btn-small" onclick="deleteSchedule('${schedule.id}')">
                    Delete
                </button>
            </div>
        </div>
    `;
}

// Cancel Task
async function cancelTask(taskId) {
    if (!confirm('Are you sure you want to cancel this task?')) {
        return;
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/tasks/${taskId}`, {
            method: 'DELETE'
        });
        
        if (response.ok) {
            alert('Task cancelled successfully');
            refreshTasks();
        } else {
            alert('Failed to cancel task');
        }
    } catch (error) {
        alert(`Error: ${error.message}`);
    }
}

// Retry removed - credentials not persisted for security
// Users should start a new migration to resume (already copied files will be skipped)

// Cleanup Tasks by Status
async function cleanupTasks(status) {
    const statusMessages = {
        'failed': 'failed tasks',
        'completed': 'completed tasks',
        'all': 'ALL non-running tasks (failed, completed, and cancelled)'
    };
    
    const message = statusMessages[status] || status + ' tasks';
    
    if (!confirm(`Are you sure you want to delete ${message}? This cannot be undone.`)) {
        return;
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/tasks/cleanup/${status}`, {
            method: 'DELETE'
        });
        
        if (response.ok) {
            const result = await response.json();
            alert(`✅ Successfully cleaned up ${result.deleted_count} ${message}`);
            refreshTasks();
        } else {
            const error = await response.json();
            alert(`Failed to cleanup tasks: ${error.error || 'Unknown error'}`);
        }
    } catch (error) {
        alert(`Error: ${error.message}`);
    }
}

// Toggle Schedule
async function toggleSchedule(scheduleId, enable) {
    const action = enable ? 'enable' : 'disable';
    
    try {
        const response = await fetch(`${API_BASE}/api/schedules/${scheduleId}/${action}`, {
            method: 'POST'
        });
        
        if (response.ok) {
            refreshSchedules();
        } else {
            alert(`Failed to ${action} schedule`);
        }
    } catch (error) {
        alert(`Error: ${error.message}`);
    }
}

// Run Schedule Now
async function runScheduleNow(scheduleId) {
    try {
        const response = await fetch(`${API_BASE}/api/schedules/${scheduleId}/run`, {
            method: 'POST'
        });
        
        if (response.ok) {
            alert('Schedule started successfully');
            setTimeout(() => {
                showTab('tasks');
                document.querySelector('[onclick*="tasks"]').click();
            }, 1000);
        } else {
            alert('Failed to start schedule');
        }
    } catch (error) {
        alert(`Error: ${error.message}`);
    }
}

// Delete Schedule
async function deleteSchedule(scheduleId) {
    if (!confirm('Are you sure you want to delete this schedule?')) {
        return;
    }
    
    try {
        const response = await fetch(`${API_BASE}/api/schedules/${scheduleId}`, {
            method: 'DELETE'
        });
        
        if (response.ok) {
            alert('Schedule deleted successfully');
            refreshSchedules();
        } else {
            alert('Failed to delete schedule');
        }
    } catch (error) {
        alert(`Error: ${error.message}`);
    }
}

// Show Create Schedule Form
function showCreateSchedule() {
    // For now, just show an alert - could be expanded to show a modal
    alert('To create a new schedule, use the Migration tab and select "Incremental Sync" → "Schedule"');
}

// Helper Functions
function showResult(elementId, type, html) {
    const resultEl = document.getElementById(elementId);
    if (resultEl) {
        resultEl.className = `result ${type}`;
        resultEl.innerHTML = html;
        resultEl.classList.remove('hidden');
    } else {
        // Fallback to alert if element doesn't exist
        const tempDiv = document.createElement('div');
        tempDiv.innerHTML = html;
        const text = tempDiv.textContent || tempDiv.innerText || '';
        if (type === 'error') {
            alert('❌ Error: ' + text);
        } else {
            alert('✅ Success: ' + text);
        }
    }
}

function resetForm() {
    const form = document.getElementById('migrationForm');
    if (form) form.reset();
    const result = document.getElementById('migrationResult');
    if (result) result.classList.add('hidden');
}

// Google Drive Authentication Functions

// Quick Google Login using public OAuth app (no setup required)
async function quickGoogleLogin() {
    try {
        // Use a public OAuth app created specifically for this migration tool
        // This allows users to login without creating their own Google Cloud Console project
        const publicClientID = "105504057171-jfkebamm68c31eah1kv0bchrrmrncmfl.apps.googleusercontent.com";
        const redirectURL = window.location.origin + '/auth/callback';
        
        // Generate OAuth URL directly in frontend
        const state = generateRandomState();
        const authURL = generateGoogleOAuthURL(publicClientID, redirectURL, state);
        
        // Store state for validation
        sessionStorage.setItem('googleDriveState', state);
        sessionStorage.setItem('googleDriveType', 'source');
        sessionStorage.setItem('googleDriveQuickLogin', 'true');
        sessionStorage.setItem('googleDrivePublicApp', 'true');
        
        // Open OAuth window
        const authWindow = window.open(
            authURL,
            'googleDriveAuth',
            'width=600,height=600,scrollbars=yes,resizable=yes'
        );
        
        // Send auth params to popup window
        authWindow.addEventListener('load', function() {
            authWindow.postMessage({
                type: 'googleDriveAuthParams',
                isPublicApp: true,
                state: state
            }, '*');
        });
        
        // Listen for tokens from popup window
        window.addEventListener('message', function(event) {
            if (event.data.type === 'googleDriveTokens') {
                const tokens = event.data.tokens;
                
                // Store tokens in the form
                document.getElementById('sourceAccessToken').value = tokens.access_token;
                document.getElementById('sourceRefreshToken').value = tokens.refresh_token;
                
                // Show folder picker
                document.getElementById('sourceFolderPicker').style.display = 'block';
                
                // Load folders
                loadGoogleDriveFolders('source');
                
                alert('✅ Google Drive authentication successful!');
            }
        });
        
        // Monitor for window close (removed due to Cross-Origin-Opener-Policy restrictions)
        // The popup will handle its own close detection and communicate via postMessage
        
    } catch (error) {
        console.error('Quick login error:', error);
        alert('❌ Failed to start Google Drive quick login: ' + error.message);
    }
}

// Generate random state for CSRF protection
function generateRandomState() {
    return Math.random().toString(36).substring(2) + Date.now().toString(36);
}

// Generate Google OAuth URL directly in frontend
function generateGoogleOAuthURL(clientID, redirectURL, state) {
    const params = new URLSearchParams({
        client_id: clientID,
        redirect_uri: redirectURL,
        response_type: 'code',
        scope: 'https://www.googleapis.com/auth/drive.readonly',
        access_type: 'offline',
        prompt: 'consent',
        state: state
    });
    
    return `https://accounts.google.com/o/oauth2/v2/auth?${params.toString()}`;
}

async function authenticateGoogleDrive(type) {
    const clientID = document.getElementById(`${type}ClientID`).value.trim();
    const clientSecret = document.getElementById(`${type}ClientSecret`).value.trim();
    const redirectURL = document.getElementById(`${type}RedirectURL`).value.trim();
    
    if (!clientID || !clientSecret || !redirectURL) {
        alert('❌ Please fill in Client ID, Client Secret, and Redirect URL first.');
        return;
    }
    
    try {
        // Get OAuth URL from backend
        const response = await fetch('/api/googledrive/auth-url', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                client_id: clientID,
                client_secret: clientSecret,
                redirect_url: redirectURL
            })
        });
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        
        const data = await response.json();
        
        // Store state for validation
        sessionStorage.setItem('googleDriveState', data.state);
        sessionStorage.setItem('googleDriveType', type);
        
        // Open OAuth window
        const authWindow = window.open(
            data.auth_url,
            'googleDriveAuth',
            'width=600,height=600,scrollbars=yes,resizable=yes'
        );
        
        // Monitor for window close
        const checkClosed = setInterval(() => {
            if (authWindow.closed) {
                clearInterval(checkClosed);
                // Check if we got tokens (user might have closed window)
                if (!document.getElementById(`${type}AccessToken`).value) {
                    console.log('OAuth window closed without completing authentication');
                }
            }
        }, 1000);
        
    } catch (error) {
        console.error('Authentication error:', error);
        alert('❌ Failed to start Google Drive authentication: ' + error.message);
    }
}

// Handle OAuth callback (called from redirect URL page)
function handleGoogleDriveCallback() {
    const urlParams = new URLSearchParams(window.location.search);
    const code = urlParams.get('code');
    const state = urlParams.get('state');
    const error = urlParams.get('error');
    
    if (error) {
        console.error('OAuth error:', error);
        alert('❌ Google Drive authentication failed: ' + error);
        return;
    }
    
    if (!code || !state) {
        console.error('Missing OAuth parameters', { code: !!code, state: !!state, url: window.location.href });
        alert('❌ Invalid OAuth response: Missing code or state parameter');
        return;
    }
    
    // Verify state
    const storedState = sessionStorage.getItem('googleDriveState');
    const type = sessionStorage.getItem('googleDriveType');
    
    if (state !== storedState) {
        console.error('State mismatch', { received: state, stored: storedState });
        alert('❌ Security error: Invalid state parameter');
        return;
    }
    
    if (!type) {
        console.error('No type stored');
        alert('❌ Authentication session expired');
        return;
    }
    
    // Exchange code for tokens
    exchangeCodeForTokens(code, type);
}

async function exchangeCodeForTokens(code, type) {
    try {
        const isQuickLogin = sessionStorage.getItem('googleDriveQuickLogin') === 'true';
        
        let requestBody;
        if (isQuickLogin) {
            const isPublicApp = sessionStorage.getItem('googleDrivePublicApp') === 'true';
            
            if (isPublicApp) {
                // Public OAuth app - no credentials needed
                requestBody = {
                    code: code
                };
            } else {
                // Custom OAuth - use stored credentials
                const clientID = sessionStorage.getItem('googleDriveClientID');
                const clientSecret = sessionStorage.getItem('googleDriveClientSecret');
                const redirectURL = sessionStorage.getItem('googleDriveRedirectURL');
                
                requestBody = {
                    client_id: clientID,
                    client_secret: clientSecret,
                    redirect_url: redirectURL,
                    code: code,
                    quick_login: true
                };
            }
        } else {
            // Custom OAuth - use user credentials
            const clientID = document.getElementById(`${type}ClientID`).value.trim();
            const clientSecret = document.getElementById(`${type}ClientSecret`).value.trim();
            const redirectURL = document.getElementById(`${type}RedirectURL`).value.trim();
            
            requestBody = {
                client_id: clientID,
                client_secret: clientSecret,
                redirect_url: redirectURL,
                code: code,
                quick_login: false
            };
        }
        
        // Choose the correct endpoint based on login type
        const endpoint = (isQuickLogin && isPublicApp) 
            ? '/api/googledrive/quick-auth-url' 
            : '/api/googledrive/exchange-token';
            
        const response = await fetch(endpoint, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(requestBody)
        });
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        
        const tokenData = await response.json();
        
        // Store tokens
        document.getElementById(`${type}AccessToken`).value = tokenData.access_token;
        document.getElementById(`${type}RefreshToken`).value = tokenData.refresh_token;
        
        // Show folder picker
        document.getElementById(`${type}FolderPicker`).style.display = 'block';
        
        // Load folders
        await loadGoogleDriveFolders(type);
        
        alert('✅ Google Drive authentication successful!');
        
        // Close popup window if this is running in one
        if (window.opener) {
            window.close();
        }
        
    } catch (error) {
        console.error('Token exchange error:', error);
        alert('❌ Failed to exchange code for tokens: ' + error.message);
    }
}

async function loadGoogleDriveFolders(type) {
    try {
        const clientID = document.getElementById(`${type}ClientID`).value.trim();
        const clientSecret = document.getElementById(`${type}ClientSecret`).value.trim();
        const accessToken = document.getElementById(`${type}AccessToken`).value.trim();
        const refreshToken = document.getElementById(`${type}RefreshToken`).value.trim();
        
        if (!accessToken || !refreshToken) {
            console.error('No tokens available');
            return;
        }
        
        const response = await fetch('/api/googledrive/list-folders', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                client_id: clientID,
                client_secret: clientSecret,
                access_token: accessToken,
                refresh_token: refreshToken,
                parent_id: '' // Root folder
            })
        });
        
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        
        const data = await response.json();
        
        // Populate folder dropdown
        const folderSelect = document.getElementById(`${type}FolderID`);
        folderSelect.innerHTML = '<option value="">Root Folder (All Files)</option>';
        
        data.folders.forEach(folder => {
            const option = document.createElement('option');
            option.value = folder.id;
            option.textContent = folder.name;
            folderSelect.appendChild(option);
        });
        
    } catch (error) {
        console.error('Load folders error:', error);
        alert('❌ Failed to load Google Drive folders: ' + error.message);
    }
}

function updateFolderPath() {
    const type = 'source'; // Assuming source for now
    const folderSelect = document.getElementById(`${type}FolderID`);
    const folderPath = document.getElementById(`${type}FolderPath`);
    
    const selectedOption = folderSelect.options[folderSelect.selectedIndex];
    folderPath.value = selectedOption.textContent;
}

// Handle Google Drive to S3 migration
async function handleGoogleDriveMigration(event, mode) {
    console.log('🚀 handleGoogleDriveMigration called!', event, mode);
    event.preventDefault();
    
    // Get Google Drive credentials
    const clientID = document.getElementById('sourceClientID').value.trim();
    const clientSecret = document.getElementById('sourceClientSecret').value.trim();
    const accessToken = document.getElementById('sourceAccessToken').value.trim();
    const refreshToken = document.getElementById('sourceRefreshToken').value.trim();
    const folderID = document.getElementById('sourceFolderID').value.trim();
    
    // Get destination S3 details
    const destBucket = document.getElementById('destBucket').value.trim();
    const destPrefix = document.getElementById('destPrefix').value.trim();
    
    // Validate required fields
    // For Quick Login (public OAuth app), only access_token and refresh_token are required
    // For Custom OAuth, client_id and client_secret are also required
    if (!accessToken || !refreshToken) {
        alert('❌ Google Drive authentication is required!\n\nPlease authenticate with Google Drive first.');
        return;
    }
    
    // If using custom OAuth (not Quick Login), client_id and client_secret are required
    if ((clientID || clientSecret) && (!clientID || !clientSecret)) {
        alert('❌ Incomplete Google Drive credentials!\n\nBoth Client ID and Client Secret are required for custom OAuth.');
        return;
    }
    
    if (!destBucket) {
        alert('❌ Destination S3 bucket is required!\n\nPlease specify the S3 bucket where files will be migrated.');
        return;
    }
    
    // Get destination S3 credentials
    const destAccessKey = document.getElementById('destAccessKey').value.trim();
    const destSecretKey = document.getElementById('destSecretKey').value.trim();
    
    if (!destAccessKey || !destSecretKey) {
        alert('❌ Destination S3 credentials are required!\n\nPlease provide Access Key and Secret Key for the destination S3 bucket.');
        return;
    }
    
    // Get destination region and endpoint
    const destProvider = document.getElementById('destProvider').value;
    let destRegion = '';
    if (S3_PROVIDERS[destProvider].requiresRegion) {
        destRegion = document.getElementById('destRegionSelect').value;
    } else {
        destRegion = document.getElementById('destRegion').value.trim();
    }
    const destEndpoint = document.getElementById('destEndpoint').value.trim();
    
    // Prepare migration data
    const migrationData = {
        source_folder_id: folderID,
        dest_bucket: destBucket,
        dest_prefix: destPrefix,
        source_credentials: {
            client_id: clientID || '', // Empty for Quick Login (public OAuth app)
            client_secret: clientSecret || '', // Empty for Quick Login (public OAuth app)
            access_token: accessToken,
            refresh_token: refreshToken,
            redirect_url: document.getElementById('sourceRedirectURL').value.trim()
        },
        dest_credentials: {
            access_key: destAccessKey,
            secret_key: destSecretKey,
            region: destRegion,
            endpoint_url: destEndpoint
        },
        dry_run: document.getElementById('dryRun').checked,
        migration_mode: mode,
        timeout: document.getElementById('timeout') ? parseInt(document.getElementById('timeout').value || 3600) : 3600,
        include_shared_files: document.getElementById('includeSharedFiles') ? document.getElementById('includeSharedFiles').checked : false
    };
    
    try {
        const response = await fetch('/api/googledrive/migrate', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(migrationData)
        });
        
        const result = await response.json();
        
        if (response.ok) {
            showResult('migrationResult', 'success', `
                <h3>✅ Google Drive Migration Started!</h3>
                <div class="result-info">
                    <div><span>Task ID:</span><code>${result.task_id}</code></div>
                    <div><span>Status:</span><span>${result.status}</span></div>
                    <div><span>Source:</span><span>Google Drive${folderID ? ` (Folder: ${document.getElementById('sourceFolderPath').value})` : ' (All Files)'}</span></div>
                    <div><span>Destination:</span><span>s3://${destBucket}/${destPrefix || ''}</span></div>
                    <div><span>Mode:</span><span>${mode === 'full_rewrite' ? 'Full Rewrite' : 'Incremental'}</span></div>
                    <div><span>Dry Run:</span><span>${migrationData.dry_run ? 'Yes' : 'No'}</span></div>
                </div>
                <p style="margin-top: 15px;">
                    <a href="#" onclick="showTab('tasks'); return false;">View in Active Tasks</a>
                </p>
            `);
            
            // Auto-switch to tasks tab
            setTimeout(() => {
                showTab('tasks');
                document.querySelector('[onclick*="tasks"]').click();
            }, 2000);
        } else {
            showResult('migrationResult', 'error', `
                <h3>❌ Error</h3>
                <p>${result.error || 'Failed to start Google Drive migration'}</p>
            `);
        }
    } catch (error) {
        console.error('Google Drive migration error:', error);
        showResult('migrationResult', 'error', `
            <h3>❌ Error</h3>
            <p>Failed to connect to API: ${error.message}</p>
        `);
    }
}

function resetScheduleForm() {
    const form = document.getElementById('scheduleForm');
    if (form) form.reset();
    const result = document.getElementById('scheduleResult');
    if (result) result.classList.add('hidden');
}

function setCron(expr) {
    document.getElementById('cronExpr').value = expr;
    return false;
}

function formatDate(dateStr) {
    if (!dateStr || dateStr === '0001-01-01T00:00:00Z') {
        return 'Never';
    }
    const date = new Date(dateStr);
    return date.toLocaleString();
}

// Calculate duration from start time to now
function getDuration(startTimeStr) {
    if (!startTimeStr) return 'Unknown';
    const startTime = new Date(startTimeStr);
    const now = new Date();
    const diffMs = now - startTime;
    
    const seconds = Math.floor(diffMs / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);
    
    if (days > 0) {
        return `${days}d ${hours % 24}h ${minutes % 60}m`;
    } else if (hours > 0) {
        return `${hours}h ${minutes % 60}m`;
    } else if (minutes > 0) {
        return `${minutes}m ${seconds % 60}s`;
    } else {
        return `${seconds}s`;
    }
}

// Date filter functionality
let allTasks = []; // Store all tasks for filtering

function applyDateFilter() {
    const fromDate = document.getElementById('filterFromDate').value;
    const toDate = document.getElementById('filterToDate').value;
    const status = document.getElementById('filterStatus').value;
    
    let filteredTasks = allTasks;
    
    // Filter by date range
    if (fromDate) {
        const from = new Date(fromDate);
        filteredTasks = filteredTasks.filter(task => {
            const taskDate = new Date(task.start_time);
            return taskDate >= from;
        });
    }
    
    if (toDate) {
        const to = new Date(toDate + 'T23:59:59'); // Include entire day
        filteredTasks = filteredTasks.filter(task => {
            const taskDate = new Date(task.start_time);
            return taskDate <= to;
        });
    }
    
    // Filter by status
    if (status) {
        filteredTasks = filteredTasks.filter(task => task.status === status);
    }
    
    // Display filtered tasks
    displayFilteredTasks(filteredTasks);
}

function clearDateFilter() {
    document.getElementById('filterFromDate').value = '';
    document.getElementById('filterToDate').value = '';
    document.getElementById('filterStatus').value = '';
    
    // Show all tasks
    displayFilteredTasks(allTasks);
}

function displayFilteredTasks(tasks) {
    const tasksList = document.getElementById('tasksList');
    
    if (!tasks || tasks.length === 0) {
        tasksList.innerHTML = '<p class="text-muted">No tasks match the filter criteria</p>';
        return;
    }
    
    // Use existing createTaskCard function for consistency
    tasksList.innerHTML = tasks.map(task => createTaskCard(task)).join('');
}

// Auto-refresh tasks handled by startAutoRefresh() function - no duplicate intervals

// Auto-refresh schedules
setInterval(() => {
    const schedulesTab = document.getElementById('schedules-tab');
    if (schedulesTab.classList.contains('active')) {
        refreshSchedules();
    }
}, 30000); // Refresh every 30 seconds if on schedules tab

