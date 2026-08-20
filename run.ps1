# Windows Application Control blocks unsigned binaries on this machine, so the
# CLI is cross-compiled for Linux and executed through WSL.
param(
    [Parameter(Position = 0)]
    [ValidateSet('build', 'test', 'analyze', 'dry-run', 'wasm')]
    [string]$Task = 'build',

    [Parameter(Position = 1)]
    [string]$Plan = 'fixtures/01-ecs-rds/plan.json',

    # Print every test as it runs. Off by default: a green run of 150+ tests is
    # two lines each of "this worked", and burying a failure in that is how a
    # failure gets scrolled past.
    [switch]$Detailed,

    # The version stamped into the wasm build. It ends up on the website beside
    # the report, so a run somebody screenshots can be traced back to a
    # release. Anything other than a published tag should say so.
    [string]$Version = 'dev'
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

# The wasm target shares no artifact with the others: it does not need the
# Linux binary and it does not need the test binaries, and building them first
# would put twenty seconds in front of a build somebody runs while editing a
# stylesheet.
if ($Task -ne 'wasm') { Invoke-Build }

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
    'wasm' {
        # The browser build of the analyzer, for the playground on the website.
        #
        # Two files come out and both are needed: the module, and the loader
        # the Go toolchain ships. The loader is copied out of GOROOT rather
        # than kept in this repository, because it has to match the compiler
        # that produced the module, and a stale copy fails at instantiation
        # with an error that says nothing useful.
        #
        # Nothing is executed here. This is the one target on this machine that
        # Application Control has no opinion about, because a wasm module is
        # not a binary this machine runs.
        Push-Location $root
        try {
            $env:GOOS = 'js'; $env:GOARCH = 'wasm'
            New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin') | Out-Null
            $out = Join-Path $root 'bin/headroom.wasm'

            # -s -w drop the symbol table and DWARF, which is a third of the
            # module and none of it reachable from a browser: the loader prints
            # a Go panic through its own trace and never reads DWARF.
            go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $out ./cmd/headroom-wasm
            if ($LASTEXITCODE -ne 0) { throw 'wasm build failed' }

            $exec = Join-Path (go env GOROOT) 'lib/wasm/wasm_exec.js'
            if (-not (Test-Path $exec)) { throw "wasm_exec.js not found at $exec" }
            Copy-Item $exec (Join-Path $root 'bin/wasm_exec.js') -Force

            $f = Get-Item $out
            $sha = (Get-FileHash $out -Algorithm SHA256).Hash.ToLower()
            "built: bin/headroom.wasm, version $Version"
            ("  size    {0:N0} bytes ({1:N1} MB)" -f $f.Length, ($f.Length / 1MB))
            "  sha256  $sha"
            "  loader  bin/wasm_exec.js, from $(go env GOVERSION)"
        }
        finally {
            $env:GOOS = ''; $env:GOARCH = ''
            Pop-Location
        }
    }
}
