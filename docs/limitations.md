# Trust model and operating limits

Flinch runs your repository's own test and lint commands on your machine, with your user's permissions. Enabling it in a repository is like running that repository's test suite. `flinch init` enables the repository's command and script checks only for the clone where you run it, and each other clone needs its own `init`.

| Boundary | Behavior | Guidance |
| --- | --- | --- |
| Child processes | Tests and linters run with the access of the Flinch process. The host's per-tool sandbox and permission prompts don't automatically apply to them. | Enable Flinch in repositories whose test suite you would run yourself. |
| Repository commands | Command and script checks defined in the repository run only in a clone where `flinch init` enabled them. | Review changes under `.flinch/checks/` like any other code that runs on your machine. |
| Filesystem links | Flinch validates repository-relative paths lexically. A symbolic link, junction or Windows reparse point in a path can redirect a file operation. | Keep check artifacts, fixtures and state in ordinary directories. |
| File permissions | Flinch replaces a file by writing a temporary file first. The replacement takes that file's mode under the operating system's creation policy, so an existing file's permission bits can change. | If a settings file needs special permissions, check them after `init`. |
| Installing several hosts | Flinch configures the hosts one by one. If a later host fails, the changes made for earlier hosts stay on disk. | Fix the reported error and run `init` again. Existing settings are kept. |
| Snapshot objects | Turn snapshots are ordinary Git objects. Clearing Flinch's local state leaves them to Git's normal maintenance. | Nothing to do: `git gc` prunes them once they are unreferenced and old enough. |
