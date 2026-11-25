# Require Flinch on pull requests

Run `flinch check --ci --base` after the repository's usual tests. It compares a protected case and its record with the PR base, so changing both a result and its expected value can still fail the job.

Save this as `.github/workflows/flinch.yml` for a Go project on an x64 Linux runner. For another stack, use the same setup and test steps as the project's CI. The installation verifies the release archive before extracting the executable. [Release installation](install.md)

```yaml
name: Flinch
on: [pull_request, merge_group]
permissions:
  contents: read
jobs:
  flinch:
    name: Flinch checks
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0
          persist-credentials: false
      - uses: actions/setup-go@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0
        with:
          go-version-file: go.mod
      - run: go test ./...
      - name: Check reviewed protections
        env:
          BASE: ${{ github.event.pull_request.base.sha || github.event.merge_group.base_sha }}
          TAG: v0.1.3
        shell: bash
        run: |
          set -euo pipefail
          git rev-parse --verify "${BASE:?missing base SHA}^{commit}" >/dev/null
          stage=$(mktemp -d)
          base="flinch_${TAG#v}_linux_amd64"
          asset="$base.tar.gz"
          url="https://github.com/angelnicolasc/flinch/releases/download/$TAG"
          for name in "$asset" checksums.txt; do
            curl -fsSL --proto '=https' --proto-redir '=https' "$url/$name" -o "$stage/$name"
          done
          (cd "$stage" && awk -v a="$asset" '$2 == a {print; n++} END {if (n != 1) exit 1}' checksums.txt | sha256sum -c -)
          tar -xzf "$stage/$asset" -C "$stage" "$base/flinch"
          "$stage/$base/flinch" check --ci --base "$BASE" --format github
```

After the first PR run, require **Flinch checks** from GitHub Actions in the target branch's protection rule or ruleset. [Required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging)

## Agents that repair pull requests

Claude's `/ship` watch bypasses repository Git hooks when it commits and pushes. The required GitHub check runs on those changes. Require code owner review and add the following to `.github/CODEOWNERS`, replacing the team with an owner who has write access. [Claude Ship](https://claude.dev/blog/the-work-after-you-open-a-pr/) · [GitHub code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)

```text
/.flinch/**   @your-org/maintainers
/.github/**   @your-org/maintainers
```

If the repository uses Claude Approvals, include `.flinch/**` in the rule for policy files under `rules` in the default branch's `.claude/approvals.yml`. This rule requires two seats and lets the author fill one. To enforce it, remove `mode: observe` and require **Claude Approvals** from the Claude app. [Approval policy reference](https://code.claude.com/docs/en/approvals-policy)

```yaml
- purpose: Changes to CI and review rules require two people.
  paths: [".flinch/**", ".github/**", "**/.claude/**", "**/CLAUDE.md", "**/REVIEW.md", "**/CODEOWNERS"]
  nested: inherit
  verdict: {claude: comments, seats: 2}
```
