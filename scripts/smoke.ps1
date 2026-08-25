# Smoke-test a tagged release, before the publish PR moves anybody onto it.
#
# This is step 2.5 of three. The gap between tagging and publishing exists so
# the built artefacts can be checked while nobody is exposed to them yet, and
# this is what fills it. Separate from check.ps1 because every check here needs
# the network, `gh` and a real `claude` - the three things the Go suite is
# hermetic against - and because none of it can run before the tags exist.
#
# What it covers is what CI structurally cannot: the -ldflags version stamp,
# which only release-workflow builds carry; the published release and its
# assets; `go install ...@vX.Y.Z` resolving against the remote; the marketplace
# ref naming a tag that exists; the plugin installing from the tag about to be
# published; and the binary and the plugin agreeing on a version. It cannot
# cover the skill actually taking an issue to a PR - see "Cutting a release" in
# the README for that half.
#
# Every check runs and the failures are summarised at the end, rather than the
# first one aborting: deciding whether to publish is easier with the whole
# picture than with one failure at a time. So $ErrorActionPreference stays at
# its default here, unlike the other scripts in this directory.
#
# Writes nothing outside a temporary directory it removes on exit - not to
# ~/.claude, ~/go/bin or ~/.backlog-drain. The plugin half installs into a
# throwaway CLAUDE_CONFIG_DIR, so the release under test never becomes the
# release this machine is running. (`go install` still populates the shared
# module cache; that is a cache, not configuration.)
#
# GitHub is asked through `gh` rather than `git ls-remote`, so the whole script
# needs one working credential instead of two - an ssh-agent that has forgotten
# its key should not read as a broken release.
param([string]$Version)

Set-Location (Join-Path $PSScriptRoot '..')

$name = 'backlog-drain'
$skill = 'implement-issue'

if (-not $Version) {
    $Version = (Get-Content .claude-plugin\plugin.json -Raw | ConvertFrom-Json).version
}
$Version = $Version -replace '^v', ''
if (-not $Version) {
    Write-Error 'could not read a version from .claude-plugin/plugin.json'
    exit 1
}

foreach ($bin in 'git', 'gh', 'claude', 'go') {
    if (-not (Get-Command $bin -ErrorAction SilentlyContinue)) {
        Write-Error "``$bin`` not found on PATH - the smoke test needs all of git, gh, claude and go"
        exit 1
    }
}

$repo = gh repo view --json nameWithOwner --jq .nameWithOwner 2>$null
if (-not $repo) {
    Write-Error 'could not read the repository from gh - is it authenticated? (gh auth login)'
    exit 1
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("smoke-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

$script:passed = 0
$script:failed = 0
$script:skipped = 0
$script:failures = @()

function Ok($what) {
    Write-Host "  ok    $what"
    $script:passed++
}
# Every failure names what to do about it, not just what went wrong: this runs
# once per release, and the operator reading it has forgotten the details.
function Bad($what, $hint) {
    Write-Host "  FAIL  $what"
    if ($hint) { Write-Host "        $hint" }
    $script:failures += $what
    $script:failed++
}
function Skip($what, $hint) {
    Write-Host "  skip  $what"
    if ($hint) { Write-Host "        $hint" }
    $script:skipped++
}
# Tools that fail with a trailing blank line would otherwise quote nothing back
# at the operator, which reads as the check failing for no reason.
function LastLine($path) {
    if (-not (Test-Path $path)) { return '' }
    $lines = Get-Content $path | Where-Object { $_.Trim() -ne '' }
    if ($lines) { return $lines[-1] }
    return ''
}

$semverTag = "v$Version"
$pluginTag = "$name--v$Version"

# Resolves a tag to the commit it names, empty if there is no such tag. The API
# dereferences annotated and lightweight tags alike, so how a tag was created
# never becomes a false alarm. Keyed on the exit status rather than the output,
# because `gh api --jq` prints the error envelope to stdout on a 404 or 422 -
# an emptiness test reads that as a resolved tag.
function TagCommit($tag) {
    $sha = gh api "repos/$repo/commits/$tag" --jq .sha 2>$null
    if ($LASTEXITCODE -ne 0) { return '' }
    return $sha
}

try {
    Write-Host "==> smoke-testing $name $Version in $repo"
    Write-Host ''

    # ------------------------------------------------------------ 1. the tags

    Write-Host 'tags'
    $semverCommit = TagCommit $semverTag
    $pluginCommit = TagCommit $pluginTag

    if ($semverCommit) {
        Ok "$semverTag is on origin"
    } else {
        Bad "$semverTag is not on origin" 'run .\scripts\release.ps1 - nothing else here can pass without it'
    }
    if ($pluginCommit) {
        Ok "$pluginTag is on origin"
    } else {
        Bad "$pluginTag is not on origin" 'run .\scripts\release.ps1 - the marketplace ref has nothing to point at'
    }

    if ($semverCommit -and $pluginCommit) {
        if ($semverCommit -eq $pluginCommit) {
            Ok 'both tags name the same commit'
        } else {
            Bad 'the two tags name different commits' `
                'the plugin and the binary would ship from different trees; delete both tags and re-run release.ps1'
        }
    }

    if ($semverCommit) {
        # Asked of GitHub rather than of the local clone, which would need a
        # fetch first - and a smoke test should not mutate refs to answer a
        # question. "behind" means main contains the tagged commit;
        # "identical" means it is main's tip.
        $status = gh api "repos/$repo/compare/main...$semverCommit" --jq .status 2>$null
        if ($LASTEXITCODE -ne 0) { $status = '' }
        switch ($status) {
            { $_ -in 'behind', 'identical' } { Ok 'the tagged commit is on main' }
            '' { Bad 'could not compare the tagged commit against main' "gh api repos/$repo/compare failed" }
            default {
                Bad "the tagged commit is not on main (compare says ""$status"")" `
                    'a release tagged off main is a release nobody can reproduce from main'
            }
        }
    }

    # The hermetic suite holds the ref to never being *ahead* of plugin.json,
    # but nothing offline can tell whether it names a tag that exists - and a
    # ref naming a deleted or never-pushed tag makes every install fail,
    # silently, until somebody tries one. This is the only place that question
    # can be asked.
    $currentRef = ((Get-Content .claude-plugin\marketplace.json -Raw | ConvertFrom-Json).plugins[0].source).ref
    if (-not $currentRef) {
        Bad 'marketplace.json declares no ref' 'installs would track a branch instead of a release'
    } elseif (TagCommit $currentRef) {
        Ok "the marketplace ref ($currentRef) names a tag that exists"
    } else {
        Bad "the marketplace ref names $currentRef, which is not on origin" `
            "every install fails on this today; moving the ref to $pluginTag is the fix"
    }

    # --------------------------------------------------------- 2. the release

    Write-Host ''
    Write-Host 'release'
    gh release view $semverTag --json tagName 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Bad "no GitHub release for $semverTag" `
            'the release workflow runs on the v* tag - check its run before going further'
    } else {
        $draft = gh release view $semverTag --json isDraft --jq .isDraft
        $pre = gh release view $semverTag --json isPrerelease --jq .isPrerelease
        if ($draft -eq 'false' -and $pre -eq 'false') {
            Ok 'the release is published, not a draft or prerelease'
        } else {
            Bad "the release is a draft or prerelease (draft=$draft prerelease=$pre)" `
                "gh release edit $semverTag --draft=false --prerelease=false"
        }

        $sizes = @{}
        foreach ($line in (gh release view $semverTag --json assets --jq '.assets[] | "\(.name) \(.size)"')) {
            $parts = $line -split ' '
            $sizes[$parts[0]] = [int]$parts[1]
        }

        $missing = @()
        $small = @()
        foreach ($target in 'linux_amd64', 'linux_arm64', 'darwin_amd64', 'darwin_arm64', 'windows_amd64.exe') {
            $asset = "${name}_${semverTag}_$target"
            if (-not $sizes.ContainsKey($asset)) {
                $missing += $asset
            } elseif ($sizes[$asset] -lt 1000000) {
                # A stdlib-only Go binary lands around 2.5MB on every target.
                # Anything under a megabyte is a truncated upload, not a
                # smaller build.
                $small += "$asset($($sizes[$asset]))"
            }
        }
        if ($missing.Count -eq 0) {
            Ok 'all five platform binaries are attached'
        } else {
            Bad "assets missing: $($missing -join ' ')" 'the build step of the release workflow did not finish'
        }
        if ($small.Count -gt 0) {
            Bad "assets implausibly small: $($small -join ' ')" 're-upload them; a truncated binary will not run'
        }

        # ----------------------------------------------------- 3. release notes

        $body = gh release view $semverTag --json body --jq .body

        # The same section the release workflow extracts, so a mismatch means
        # the workflow fell back to --generate-notes rather than that the two
        # disagree about how to read a changelog.
        $notes = @()
        $inSection = $false
        foreach ($line in (Get-Content CHANGELOG.md)) {
            if ($line -match "^## \[?$([regex]::Escape($Version))\]?") { $inSection = $true; continue }
            if ($inSection -and $line -match '^## ') { break }
            if ($inSection) { $notes += $line }
        }

        # Trailing whitespace and blank lines at either end survive a round
        # trip through the API inconsistently, and none of them are a release
        # problem.
        function Norm($lines) {
            $trimmed = @($lines | ForEach-Object { $_ -replace '\s+$', '' })
            while ($trimmed.Count -gt 0 -and $trimmed[0] -eq '') { $trimmed = $trimmed[1..($trimmed.Count - 1)] }
            while ($trimmed.Count -gt 0 -and $trimmed[-1] -eq '') { $trimmed = $trimmed[0..($trimmed.Count - 2)] }
            return ($trimmed -join "`n")
        }

        if ($notes.Count -eq 0) {
            Bad "CHANGELOG.md has no section for $Version" `
                'the release body will be generated commit subjects; write the section and edit the release'
        } elseif ((Norm $notes) -eq (Norm ($body -split "`r?`n"))) {
            Ok "the release body is the changelog section for $Version"
        } else {
            Bad "the release body is not the changelog section for $Version" `
                "the workflow fell back to --generate-notes; gh release edit $semverTag --notes-file CHANGELOG-section"
        }
    }

    # ---------------------------------------------------- 4. the built binary

    Write-Host ''
    Write-Host 'binary'
    # What a released binary must print. The stamp is `v0.6.0`, not `0.6.0`:
    # GITHUB_REF_NAME carries the prefix and drainVersion returns it untouched.
    $want = "$name $semverTag"
    $drain = ''
    $asset = "${name}_${semverTag}_windows_amd64.exe"

    gh release download $semverTag --pattern $asset --dir "$tmp\dl" 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Bad "could not download $asset" 'gh release download failed'
    } else {
        $got = & "$tmp\dl\$asset" -version 2>&1 | Out-String
        $got = $got.Trim()
        if ($got -eq $want) {
            $drain = "$tmp\dl\$asset"
            Ok "the downloaded windows/amd64 binary reports ""$want"""
        } else {
            Bad "the downloaded binary reports ""$got"", not ""$want""" `
                'the -ldflags stamp in the release workflow is wrong; every recorded run would be misattributed'
        }
    }

    if ($drain) {
        New-Item -ItemType Directory -Path "$tmp\metrics" -Force | Out-Null
        $out = & $drain stats -metrics "$tmp\metrics" 2>&1 | Out-String
        if ($LASTEXITCODE -eq 0 -and $out -match 'no run data') {
            Ok '`stats` reads an empty run-data directory'
        } else {
            Bad '`stats` failed on an empty run-data directory' (($out -split "`r?`n")[0])
        }
    }

    # -------------------------------------------------------- 5. go install

    Write-Host ''
    Write-Host 'go install'
    # Go fetches a module over https, which on a private repo needs a
    # credential the operator may only have configured for ssh - and whether
    # *this machine* can clone over https is not what this check is about.
    # Borrowing gh's token for the one command, through GIT_CONFIG_* so nothing
    # is written to any gitconfig, keeps the check about the tag resolving.
    $ghToken = gh auth token 2>$null
    if ($ghToken) {
        $env:GIT_CONFIG_COUNT = '1'
        $env:GIT_CONFIG_KEY_0 = "url.https://x-access-token:$ghToken@github.com/.insteadOf"
        $env:GIT_CONFIG_VALUE_0 = 'https://github.com/'
    }
    $owner = $repo.Split('/')[0]
    $env:GOPRIVATE = "github.com/$owner/*"
    $env:GOBIN = "$tmp\gobin"
    go install "github.com/$repo/cmd/$name@$semverTag" *> "$tmp\goinstall.log"
    $installed = $LASTEXITCODE -eq 0
    Remove-Item Env:\GIT_CONFIG_COUNT, Env:\GIT_CONFIG_KEY_0, Env:\GIT_CONFIG_VALUE_0, Env:\GOPRIVATE, Env:\GOBIN `
        -ErrorAction SilentlyContinue

    if ($installed) {
        $got = (& "$tmp\gobin\$name.exe" -version 2>&1 | Out-String).Trim()
        if ($got -eq $want) {
            Ok "``go install ...@$semverTag`` resolves and reports ""$want"""
        } else {
            Bad "``go install ...@$semverTag`` reports ""$got"", not ""$want""" `
                "the module version is not the tag; check that $semverTag is a plain semver tag on the module root"
        }
    } elseif ((Get-Content "$tmp\goinstall.log" -Raw) -match 'unknown revision|invalid version') {
        Bad "``go install ...@$semverTag`` cannot resolve that version" (LastLine "$tmp\goinstall.log")
    } else {
        Bad "``go install ...@$semverTag`` failed" (LastLine "$tmp\goinstall.log")
    }

    # --------------------------------------------------------- 6. the plugin

    Write-Host ''
    Write-Host 'plugin'
    # Everything below runs against a throwaway config directory, so installing
    # the release under test does not move this machine onto it, and does not
    # disturb the marketplace already registered at user scope.
    $env:CLAUDE_CONFIG_DIR = "$tmp\claude"
    New-Item -ItemType Directory -Path $env:CLAUDE_CONFIG_DIR -Force | Out-Null

    # The marketplace is added from a local copy with the ref already moved,
    # because that - not the tag - is the configuration under test. Adding the
    # remote marketplace pinned at $pluginTag would read the marketplace.json
    # *in* that tag, whose ref still names the previous release: no tag ever
    # contains its own ref, since the publish commit lands after the tag is
    # cut. So this rehearses exactly what the publish PR is about to make true,
    # one step early.
    New-Item -ItemType Directory -Path "$tmp\mkt\.claude-plugin" -Force | Out-Null
    $mkt = Get-Content .claude-plugin\marketplace.json -Raw
    $mkt = $mkt -replace '"ref"\s*:\s*"[^"]*"', """ref"": ""$pluginTag"""
    Set-Content -Path "$tmp\mkt\.claude-plugin\marketplace.json" -Value $mkt

    $marketplace = (Get-Content .claude-plugin\marketplace.json -Raw | ConvertFrom-Json).name
    $pluginInstalled = $false

    claude plugin marketplace add "$tmp\mkt" *> "$tmp\marketplace.log"
    if ($LASTEXITCODE -ne 0) {
        Bad "could not add a marketplace pinning $pluginTag" (LastLine "$tmp\marketplace.log")
    } else {
        claude plugin install "$name@$marketplace" --yes *> "$tmp\install.log"
        if ($LASTEXITCODE -ne 0) {
            Bad "could not install $name@$marketplace from $pluginTag" (LastLine "$tmp\install.log")
        } else {
            $pluginInstalled = $true
            Ok "the plugin installs with the ref moved to $pluginTag"

            # Keyed on this plugin's id rather than on being first in the
            # array: the manifest is a list, and reading whichever version came
            # out on top would quietly vouch for a different plugin than the
            # one under test.
            $entry = (claude plugin list --json | ConvertFrom-Json) `
            | Where-Object { $_.id -eq "$name@$marketplace" } | Select-Object -First 1
            $got = $entry.version
            if ($got -eq $Version) {
                Ok "the installed plugin reports $Version"
            } else {
                Bad "the installed plugin reports ""$got"", not ""$Version""" `
                    'plugin.json and the tag disagree; Claude Code caches by version, so this is what users would be pinned to'
            }

            # Only the inventory counts. The plugin's own description names the
            # skill too - "/implement-issue takes a single issue from plan to
            # PR" - so a search of the whole output would report a shipped
            # skill for a plugin that ships none, which is the one thing this
            # check exists to notice.
            $details = @(claude plugin details $name)
            $at = ($details | Select-String -Pattern '^Component inventory' | Select-Object -First 1).LineNumber
            $inventory = if ($at) { ($details | Select-Object -Skip ($at - 1)) -join "`n" } else { '' }
            if ($inventory -match [regex]::Escape($skill)) {
                Ok "its component inventory lists $skill"
            } else {
                Bad "its component inventory does not list $skill" `
                    "the skill did not ship in the tagged tree; check skills/$skill/SKILL.md at $pluginTag"
            }
        }
    }

    # ------------------------------------------- 7. the two halves, together

    Write-Host ''
    Write-Host 'the pair'
    if (-not $drain) {
        Skip 'version-skew check' 'no downloaded binary to run'
    } elseif (-not $pluginInstalled) {
        Skip 'version-skew check' 'the plugin did not install'
    } else {
        # A label no issue carries makes this a preflight-only run: preflight
        # does the PATH, git, gh and version-skew checks, lowestOpenIssue then
        # finds nothing and the process exits 0 without starting a single
        # claude run. -metrics off keeps smoke runs out of the real run data.
        $out = & $drain -dir . -label "__$name-smoke__" -metrics off 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) {
            Bad 'preflight failed' (($out -split "`r?`n" | Where-Object { $_.Trim() })[-1])
        } else {
            if ($out -match 'version skew') {
                Bad 'the binary and the plugin disagree on a version' `
                    (($out -split "`r?`n" | Select-String 'version skew')[0].ToString())
            } else {
                Ok "binary $Version and plugin $Version agree - no skew warning"
            }
            if ($out -match [regex]::Escape($repo)) {
                Ok "preflight reaches $repo"
            } else {
                Bad 'preflight did not name the repository' (($out -split "`r?`n")[0])
            }
        }
    }

    # A skill missing from the session inventory is what execClaude's
    # lacksCommand exists to catch: the run that burns a turn achieving
    # nothing. The init event is the only place a session says what it has.
    #
    # Judged on whether that event arrived, not on the exit status: a fresh
    # CLAUDE_CONFIG_DIR has no credentials, so the process still emits its
    # inventory and then fails the turn. The inventory is all this needs, and
    # skipping over an unauthenticated config dir would skip the check
    # everywhere.
    if (-not $pluginInstalled) {
        Skip "session lists /${name}:$skill" 'the plugin did not install'
    } else {
        claude -p 'hi' --output-format stream-json --verbose > "$tmp\init.jsonl" 2> "$tmp\init.err"
        $init = Get-Content "$tmp\init.jsonl" -ErrorAction SilentlyContinue `
        | Where-Object { $_ -match '"slash_commands"' } | Select-Object -First 1
        if (-not $init) {
            Skip "session lists /${name}:$skill" `
                'claude emitted no init event - check by hand with CLAUDE_CONFIG_DIR set'
        } elseif ($init -match """${name}:$skill""") {
            Ok "a session lists /${name}:$skill"
        } else {
            Bad "a session does not list /${name}:$skill" `
                "an unattended run would burn a turn doing nothing; check the skill's frontmatter at $pluginTag"
        }
    }

    # ------------------------------------------------------------- summary

    Write-Host ''
    Write-Host "==> $script:passed passed, $script:failed failed, $script:skipped skipped"
    if ($script:failed -gt 0) {
        Write-Host ''
        Write-Host 'failed:'
        foreach ($f in $script:failures) { Write-Host "  - $f" }
        Write-Host ''
        Write-Host "Nobody is on $Version yet, which is the point of checking here: fix what is"
        Write-Host 'listed above before the publish PR moves the marketplace ref onto it.'
        exit 1
    }

    Write-Host ''
    Write-Host 'The skill half is not covered by any of this. Before opening the publish PR,'
    Write-Host "drive one real issue through $Version - see ""Cutting a release"" in the README."
    Write-Host ''
    Write-Host "Then move the marketplace entry's ref to $pluginTag."
} finally {
    Remove-Item Env:\CLAUDE_CONFIG_DIR -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
