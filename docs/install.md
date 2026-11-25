# Install Flinch

Install one executable for your user account, review its hook commands, and confirm the installation. Open a local checkout in the host you use. Claude Code and Antigravity also accept an explicit clone grant when the host supplies no trust signal. [Host trust and approval screens](hosts.md)

## Instructions for an agent

1. Identify the operating system and CPU architecture. Select a stable release from the official repository. If GitHub has no release with the matching archive and checksum file, report that and stop. [Flinch releases](https://github.com/angelnicolasc/flinch/releases)
2. Download the archive and `checksums.txt` from the **same tag**. Verify the SHA-256 for that exact filename before extracting anything. The platform instructions below install the verified executable at a stable user path. An existing Homebrew or Scoop installation can use its package-manager upgrade instead.
3. Run the installed executable with `setup --plan` from the intended checkout. To install for one host, use `setup --host claude --plan`, substituting `codex` or `antigravity` as needed. Show the developer the entire text, including the executable, files, commands, trust behavior, and shutdown options. Ask whether to apply that plan and wait for an explicit yes.
4. After confirmation, run the same command with `--yes` in place of `--plan`. A person in an interactive terminal can run `flinch setup` to review and confirm in one step. If the project or selected host configuration changes, review the updated plan first.
5. Run `flinch doctor` in the checkout. Report the installed hooks, which hosts can act in that directory, and any separate review the host requests. Restart the host session if its hook list has not refreshed.

## Homebrew and Scoop

With Homebrew on macOS or Linux:

```sh
brew install angelnicolasc/tap/flinch
flinch setup --plan
```

For Scoop on Windows, register the bucket once and install its package.

```powershell
scoop bucket add angelnicolasc https://github.com/angelnicolasc/scoop-bucket
scoop install angelnicolasc/flinch
flinch setup --plan
```

Confirm and run `flinch setup --yes`, then `flinch doctor`. Future upgrades use `brew upgrade flinch` or `scoop update flinch`. The user hook keeps pointing at the Homebrew stable executable or the Scoop shim. When a manifest for the release is unavailable, use the archive instructions below.

## Linux and macOS release archives

Run this from the repository where you want to inspect the plan. It needs `curl`, `tar`, and the system SHA-256 utility. It installs to `$HOME/.local/bin/flinch` and prints the plan without changing any hooks.

<!-- install-block: posix-archive -->
```sh
(
  set -eu
  case "$(uname -s)" in
    Linux) platform=linux ;;
    Darwin) platform=darwin ;;
    *) echo "Choose the Windows instructions for Windows." >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "This CPU has no release archive." >&2; exit 1 ;;
  esac
  if [ "$platform" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
  repo=https://github.com/angelnicolasc/flinch
  stage=$(mktemp -d)
  trap 'rm -rf -- "$stage"' EXIT
  latest=$(curl -fsSIL --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "$repo/releases/latest")
  tag=${latest##*/}
  case "$tag" in v[0-9]*) ;; *) echo "A tagged release is required." >&2; exit 1 ;; esac
  base="flinch_${tag#v}_${platform}_${arch}"
  asset="$base.tar.gz"
  for name in "$asset" checksums.txt; do
    curl -fsSL --proto '=https' --proto-redir '=https' -o "$stage/$name" "$repo/releases/download/$tag/$name"
  done
  expected=$(awk -v name="$asset" '$2 == name {print $1; n++} END {if (n != 1) exit 1}' "$stage/checksums.txt")
  case "$platform" in
    linux) actual=$(sha256sum "$stage/$asset" | awk '{print $1}') ;;
    darwin) actual=$(shasum -a 256 "$stage/$asset" | awk '{print $1}') ;;
  esac
  if [ -z "$actual" ] || [ "$expected" != "$actual" ]; then
    echo "Archive SHA-256 mismatch." >&2
    exit 1
  fi
  tar -xzf "$stage/$asset" -C "$stage" "$base/flinch"
  install -d "$HOME/.local/bin"
  install -m 0755 "$stage/$base/flinch" "$HOME/.local/bin/flinch"
  "$HOME/.local/bin/flinch" setup --plan
)
```

After confirmation run `"$HOME/.local/bin/flinch" setup --yes` and `"$HOME/.local/bin/flinch" doctor`. The absolute path works even when the user bin directory is absent from `PATH`. The macOS release binary is unnotarized. If Gatekeeper quarantines the verified download, follow the instructions attached to that release.

## Windows release archives

Run this in PowerShell. It selects the architecture of Windows, verifies the downloaded ZIP, installs `%LOCALAPPDATA%\Flinch\bin\flinch.exe`, and prints the plan.

<!-- install-block: windows-archive -->
```powershell
$ErrorActionPreference = 'Stop'
$arch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw 'This CPU has no release archive.' }
}
$repo = 'https://github.com/angelnicolasc/flinch'
$curlOptions = @('--disable', '--fail', '--silent', '--show-error', '--location', '--tlsv1.2',
                 '--proto', '=https', '--proto-redir', '=https')
$latest = curl.exe @curlOptions --head --output NUL --write-out '%{url_effective}' "$repo/releases/latest"
if ($LASTEXITCODE -ne 0) { throw 'Could not resolve the latest release.' }
if ($latest -cmatch '^https://github\.com/angelnicolasc/flinch/releases/tag/(v[0-9][0-9A-Za-z.-]*)$') {
    $tag = $Matches[1]
} else { throw 'A tagged Flinch release is required.' }
$base = "flinch_$($tag.Substring(1))_windows_$arch"
$asset = "$base.zip"
$releaseUrl = "$repo/releases/download/$tag"
$checksums = curl.exe @curlOptions "$releaseUrl/checksums.txt"
if ($LASTEXITCODE -ne 0) { throw 'Could not download the release checksums.' }
$expected = @(foreach ($line in $checksums) {
    if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+)$' -and $Matches[2] -ceq $asset) { $Matches[1] }
})
if ($expected.Count -ne 1) { throw "Expected one checksum for $asset." }
$zip = [IO.Path]::GetTempFileName()
$staged = $null
try {
    curl.exe @curlOptions --output $zip "$releaseUrl/$asset"
    if ($LASTEXITCODE -ne 0) { throw "Could not download $asset." }
    if ((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -ne $expected[0]) {
        throw 'Archive SHA-256 mismatch.'
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($zip)
    try {
        $entries = @($archive.Entries | Where-Object { $_.FullName -ceq "$base/flinch.exe" })
        if ($entries.Count -ne 1) { throw "Expected one $base/flinch.exe entry in $asset." }
        $binDir = Join-Path $env:LOCALAPPDATA 'Flinch\bin'
        [IO.Directory]::CreateDirectory($binDir) | Out-Null
        $flinch = Join-Path $binDir 'flinch.exe'
        $staged = Join-Path $binDir ([Guid]::NewGuid().ToString() + '.tmp')
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], $staged)
    } finally { $archive.Dispose() }
    if ([IO.File]::Exists($flinch)) {
        [IO.File]::Replace($staged, $flinch, [System.Management.Automation.Language.NullString]::Value)
    } else {
        [IO.File]::Move($staged, $flinch)
    }
} finally {
    [IO.File]::Delete($zip)
    if ($staged) { [IO.File]::Delete($staged) }
}
& $flinch setup --plan
```

After confirmation run `& $flinch setup --yes` and `& $flinch doctor` in the same PowerShell window. For future releases, replace the executable at the same path.

Codex and Antigravity register a readable absolute executable path on Windows. If its spelling includes spaces or shell punctuation, setup tries its short Windows path. If neither spelling works, choose a user-controlled installation directory with simple names and rerun the plan. Setup checks the path before changing host files.

## Control or remove the installation

- Run `flinch enable` in a reviewed clone to grant Claude Code and Antigravity access to it and its linked worktrees. `flinch disable` revokes that permission. Codex always uses its own workspace trust.
- Run `flinch pause` in a checkout to pause it. `flinch resume` clears the pause. Use `FLINCH=off` in the host's session environment to disable Flinch there.
- Run `flinch allow-commands` only after reviewing the repository's command and script definitions. Content changes need fresh permission.
- Run `flinch setup --remove` to review and remove the user hooks and revoke their setup consent. Use `--host` to limit removal. Uninstall the binary with its package manager or remove the installed executable after detaching the hooks.
- For a team that explicitly wants hook files in its repository, use `flinch init --shared --hooks <host>`. When both scopes register the event, the shared handler yields to the installed user handler. Review the user handler in hosts that require approval.
