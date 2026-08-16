# Windows Application Control blocks unsigned binaries on this machine, so the
# CLI is cross-compiled for Linux and executed through WSL.
param(
    [Parameter(Position = 0)]
    [ValidateSet('build', 'test', 'analyze', 'dry-run')]
    [string]$Task = 'build',

    [Parameter(Position = 1)]
    [string]$Plan = 'fixtures/01-ecs-rds/plan.json',

    # Print every test as it runs. Off by default: a green run of 150+ tests is
    # two lines each of "this worked", and burying a failure in that is how a
    # failure gets scrolled past.
    [switch]$Detailed
)

$ErrorActionPreference = 'Stop'
$env:Path += ';C:\Users\Marcus\sdk\go\bin'
$root = $PSScriptRoot
$wslRoot = '/mnt/' + $root.Substring(0, 1).ToLower() + $root.Substring(2).Replace('\', '/')

# Every package that holds tests. This used to be the single hardcoded
# internal/rules, which is how seven packages, the CLI among them, went a whole
# build without one test running against them.
function Get-TestPackages {
    Get-ChildItem -Path $root -Recurse -Filter '*_test.go' -File |
        Where-Object { $_.FullName -notmatch '\\(bin|\.terraform)\\' } |
        ForEach-Object { Split-Path $_.FullName -Parent } |
        Sort-Object -Unique |
        ForEach-Object { $_.Substring($root.Length + 1).Replace('\', '/') }
}

function Invoke-Build {
    Push-Location $root
    try {
        $env:GOOS = 'linux'; $env:GOARCH = 'amd64'
        go build -o bin/headroom-linux ./cmd/headroom
        if ($LASTEXITCODE -ne 0) { throw 'build failed' }

        foreach ($pkg in Get-TestPackages) {
            $name = ($pkg -replace '[/\\]', '-') + '.test'
            go test -c -o "bin/$name" "./$pkg"
            if ($LASTEXITCODE -ne 0) { throw "test build failed: $pkg" }
        }
    }
    finally {
        $env:GOOS = ''; $env:GOARCH = ''
        Pop-Location
    }
}

Invoke-Build

switch ($Task) {
    'build' {
        $pkgs = Get-TestPackages
        "built: bin/headroom-linux, and $($pkgs.Count) test binaries ($($pkgs -join ', '))"
    }
    'test' {
        # Each binary runs with its own package directory as the working
        # directory, because the tests reach for ../../fixtures relative to
        # where their source lives.
        #
        # The binaries are executed directly rather than through `go test`, which
        # is the whole point of this script and also the reason the output is raw:
        # colour and the per-package summary are things the go tool prints, and
        # the go tool never runs here. So this does it instead. Everything below
        # is presentation: the verdict is $LASTEXITCODE and nothing else.
        $failed = @()
        $passedTotal = 0
        $skippedTotal = 0

        foreach ($pkg in Get-TestPackages) {
            $name = ($pkg -replace '[/\\]', '-') + '.test'
            $out = wsl -e bash -c "cd $wslRoot/$pkg && $wslRoot/bin/$name -test.v"
            $ok = $LASTEXITCODE -eq 0

            $passed = @($out | Select-String -Pattern '^\s*--- PASS').Count
            $skipped = @($out | Select-String -Pattern '^\s*--- SKIP').Count
            $failures = @($out | Select-String -Pattern '^\s*--- FAIL')
            $passedTotal += $passed
            $skippedTotal += $skipped

            if ($ok) {
                Write-Host ('  ok    ' + $pkg.PadRight(20) + ' ' + "$passed".PadLeft(4) + ' passed') `
                    -ForegroundColor Green -NoNewline
                if ($skipped -gt 0) { Write-Host "  $skipped skipped" -ForegroundColor DarkYellow -NoNewline }
                Write-Host ''
            }
            else {
                $failed += $pkg
                Write-Host ('  FAIL  ' + $pkg.PadRight(20) + ' ' + "$($failures.Count)".PadLeft(4) + ' failed, ' + "$passed passed") `
                    -ForegroundColor Red
            }

            # A passing test says nothing worth reading. A failing one says
            # everything, so its output is never summarised away.
            if ($Detailed -or -not $ok) {
                foreach ($line in $out) {
                    $text = [string]$line
                    if ($text -match '^\s*--- FAIL|^FAIL|^panic:') { Write-Host "    $text" -ForegroundColor Red }
                    elseif ($text -match '^\s*--- PASS|^ok\s|^PASS$') {
                        if ($Detailed) { Write-Host "    $text" -ForegroundColor DarkGreen }
                    }
                    elseif ($text -match '^\s*--- SKIP') { Write-Host "    $text" -ForegroundColor DarkYellow }
                    elseif ($text -match '^=== RUN|^=== PAUSE|^=== CONT') {
                        if ($Detailed) { Write-Host "    $text" -ForegroundColor DarkGray }
                    }
                    else { Write-Host "    $text" }
                }
            }
        }

        Write-Host ''
        if ($failed.Count -gt 0) {
            Write-Host ("  $passedTotal passed, and " + $failed.Count + ' package(s) failed: ' + ($failed -join ', ')) -ForegroundColor Red
            throw "FAILED: $($failed -join ', ')"
        }
        $summary = "  $passedTotal passed"
        if ($skippedTotal -gt 0) { $summary += ", $skippedTotal skipped" }
        Write-Host ($summary + ', 0 failed') -ForegroundColor Green
    }
    'analyze' {
        wsl -e bash -c "$wslRoot/bin/headroom-linux analyze $wslRoot/$($Plan.Replace('\','/'))"
    }
    'dry-run' {
        wsl -e bash -c "$wslRoot/bin/headroom-linux analyze --dry-run --salt local-dev $wslRoot/$($Plan.Replace('\','/'))"
    }
}
