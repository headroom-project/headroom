# Windows Application Control blocks unsigned binaries on this machine, so the
# CLI is cross-compiled for Linux and executed through WSL.
param(
    [Parameter(Position = 0)]
    [ValidateSet('build', 'test', 'analyze', 'dry-run')]
    [string]$Task = 'build',

    [Parameter(Position = 1)]
    [string]$Plan = 'fixtures/01-ecs-rds/plan.json'
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
        $failed = @()
        foreach ($pkg in Get-TestPackages) {
            $name = ($pkg -replace '[/\\]', '-') + '.test'
            "=== $pkg ==="
            wsl -e bash -c "cd $wslRoot/$pkg && $wslRoot/bin/$name -test.v"
            if ($LASTEXITCODE -ne 0) { $failed += $pkg }
        }
        if ($failed.Count -gt 0) {
            throw "FAILED: $($failed -join ', ')"
        }
    }
    'analyze' {
        wsl -e bash -c "$wslRoot/bin/headroom-linux analyze $wslRoot/$($Plan.Replace('\','/'))"
    }
    'dry-run' {
        wsl -e bash -c "$wslRoot/bin/headroom-linux analyze --dry-run --salt local-dev $wslRoot/$($Plan.Replace('\','/'))"
    }
}
