# Security policy

## Supported versions

Flinch is pre-1.0. Security fixes go into the latest release only.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Please don't open a public issue for a security problem.

Include the Flinch version, your operating system, the host and the steps to reproduce. You should hear back within a week. Once a fix is ready, it ships in a release whose notes credit you, unless you'd rather stay anonymous.

## What Flinch runs on your machine

Flinch runs your repository's own test and lint commands, locally and with your user's permissions. Command and script checks defined by a repository run only in a clone where `flinch init` enabled them. The engine makes no network calls of its own and stores its state under the repository's Git directory. [Trust model and operating limits](docs/limitations.md) describes the boundaries in detail.

## Scope

In scope:
- a path that escapes the repository or Flinch's state directory;
- a repository command that runs in a clone that never enabled commands;
- a way for a turn to delete or weaken a reviewed check without Flinch reporting it;
- prompt or session data written somewhere other than the documented state.

Out of scope:
- behavior already listed in [operating limits](docs/limitations.md), such as symbolic links or file permissions;
- what your own test suite does when Flinch runs it.
