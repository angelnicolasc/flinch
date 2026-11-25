# Security policy

## Supported versions

Flinch is pre-1.0. Security fixes go into the latest release only.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Please don't open a public issue for a security problem.

Include the Flinch version, your operating system, the host and the steps to reproduce. You should hear back within a week. Once a fix is ready, it ships in a release whose notes credit you, unless you'd rather stay anonymous.

## What Flinch runs on your machine

After setup, Flinch's user hooks run a checkout's own tests and linters with your user's permissions, and only where the host trusts the workspace or you ran `flinch enable`. Command and script checks defined by the repository also need your approval in that clone with `flinch allow-commands`, bound to their contents. Flinch's hook engine makes no network calls of its own, but the tests and linters it runs keep their network access. Flinch stores its state under the repository's Git directory and your user configuration directory. [Trust model and operating limits](docs/limitations.md) describes the boundaries in detail.

## Scope

In scope:
- a repository that makes Flinch act in a workspace the host doesn't trust and the developer never enabled;
- a path that escapes the repository or Flinch's state directory;
- a repository command that runs in a clone that never approved it, or after its contents changed;
- a way for a turn to delete or weaken a reviewed check without Flinch reporting it;
- prompt or session data written somewhere other than the documented state.

Out of scope:
- behavior already listed in [operating limits](docs/limitations.md), such as symbolic links or file permissions;
- what your own test suite does when Flinch runs it.
