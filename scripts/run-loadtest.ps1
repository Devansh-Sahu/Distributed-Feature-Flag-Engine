# ─────────────────────────────────────────────────────────────────────────
# FFEE — Load Test Automation Script (ASCII Logging Only)
# Runs a 100K evaluations/sec proof using Go SDK in-memory evaluation
# ─────────────────────────────────────────────────────────────────────────

$ErrorActionPreference = "Continue"

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "  Starting FFEE Load Test Runner (k6)" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan

# 1. Ensure core stack is running and healthy
Write-Host ""
Write-Host "[1/5] Ensuring core FFEE service stack is running..." -ForegroundColor Blue
docker compose up -d

Write-Host "Waiting for FFEE API server to be healthy..."
$serverHealthy = $false
for ($i = 0; $i -lt 30; $i++) {
    try {
        $res = Invoke-RestMethod -Uri "http://localhost:8080/health" -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop
        if ($res.status -eq "ok") {
            $serverHealthy = $true
            Write-Host "[OK] FFEE Server is healthy." -ForegroundColor Green
            break
        }
    } catch {
        Write-Host "." -NoNewline
        Start-Sleep -Seconds 1
    }
}

if (-not $serverHealthy) {
    Write-Host ""
    Write-Host "Error: FFEE Server failed to start or is unhealthy." -ForegroundColor Red
    exit 1
}

# 2. Seed a test flag if it does not exist
Write-Host ""
Write-Host "[2/5] Seeding test flag 'new-checkout-flow'..." -ForegroundColor Blue
try {
    $body = @{
        key = "new-checkout-flow"
        name = "New Checkout Flow"
        flag_type = "boolean"
    } | ConvertTo-Json
    
    $res = Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/flags" -ContentType "application/json" -Body $body -UseBasicParsing -ErrorAction Stop
    Write-Host "[OK] Created flag 'new-checkout-flow'." -ForegroundColor Green
} catch {
    Write-Host "[*] Flag 'new-checkout-flow' already exists or creation skipped." -ForegroundColor Yellow
}

# Enable the flag with 50% rollout for production
try {
    $configBody = @{
        enabled = $true
        rollout_percentage = 50
    } | ConvertTo-Json
    
    $res = Invoke-RestMethod -Method Patch -Uri "http://localhost:8080/api/v1/flags/new-checkout-flow/config/production" -ContentType "application/json" -Body $configBody -UseBasicParsing -ErrorAction Stop
    Write-Host "[OK] Configured 'new-checkout-flow' with 50% rollout in production." -ForegroundColor Green
} catch {
    Write-Host "[*] Note: Could not update flag configuration (already configured or DB error)." -ForegroundColor Yellow
}

# 3. Build and start the loadtest mock app
Write-Host ""
Write-Host "[3/5] Building and launching Go SDK evaluation mock app..." -ForegroundColor Blue
docker compose --profile loadtest build loadtest-app
docker compose --profile loadtest up -d loadtest-app

Write-Host "Waiting for loadtest-app to boot on port 8085..."
$appHealthy = $false
for ($i = 0; $i -lt 15; $i++) {
    try {
        $res = Invoke-RestMethod -Uri "http://localhost:8085/health" -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop
        if ($res.status -eq "ok") {
            $appHealthy = $true
            Write-Host ""
            Write-Host "[OK] loadtest-app is healthy." -ForegroundColor Green
            break
        }
    } catch {
        Write-Host "." -NoNewline
        Start-Sleep -Seconds 1
    }
}

if (-not $appHealthy) {
    Write-Host ""
    Write-Host "Error: loadtest-app failed to start or report healthy." -ForegroundColor Red
    exit 1
}

# 4. Run the k6 Load Test
Write-Host ""
Write-Host "[4/5] Executing k6 load test container..." -ForegroundColor Blue
Write-Host "This will simulate 300 VUs performing continuous local evaluations for 30 seconds." -ForegroundColor Gray
docker compose --profile loadtest run --rm k6

# 5. Summary & Teardown
Write-Host ""
Write-Host "[5/5] Load test execution complete." -ForegroundColor Blue
Write-Host "Stopping loadtest services..." -ForegroundColor Gray
docker compose --profile loadtest stop loadtest-app

Write-Host ""
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "  Load Test Run Successfully Completed!" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
