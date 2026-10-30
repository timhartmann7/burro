# Your agent reads 30 lines, not 30 MB.

**burro** runs your coding agent's tests on your own server. One call to start, one short summary back. Works with Claude Code, Codex and Cursor.

```sh
curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh
```

## Your agent drives it

`burro init` writes a short rules block into the project's `CLAUDE.md` or `AGENTS.md`. From then on the agent runs tests through burro on its own. Rather paste it yourself? `burro init --print` prints the block and writes nothing.

```markdown
<!-- burro:begin -->
## Remote runs

- Tests, builds and e2e run on the burro host only, through `burro`.
  Never use raw `ssh`, `rsync` or `docker` against that host.
- Start a run with `burro run <pipeline>`. Then run `burro wait` as a
  background command and keep working. Act on its summary.
- If you can't run background commands, run `burro wait --timeout 9m`
  and repeat it while it exits with 3.
- Never poll with sleep loops.
- Need more detail: `burro log --step <name> --grep <pattern>`.
  Never print a whole log.
- One-off commands on the host: `burro exec -- <cmd>` or
  `burro exec --file <script>`. No nested quoting through ssh.
- Exit 1: fix the code. Exit 2: the stand is broken, report it and
  stop. Exit 5: a run is active, wait for it or `burro cancel`.
- Never clean docker by hand. Use `burro clean`.
- burro.toml has no test steps yet: add them from the project's test
  commands, each with a JUnit report. Field reference: `burro help config`.
- New test commands go into burro.toml with a JUnit report, not into
  ad hoc scripts.
<!-- burro:end -->
```

burro is a pack donkey: it carries your e2e suite to the server and brings back only what the agent needs to read.

## Quick start

You need macOS or Linux with `ssh`, `rsync` and `git`, and a Linux server on amd64 or arm64 with `rsync` and `bash`.

```sh
burro host add myserver root@203.0.113.10 -i ~/.ssh/id_ed25519   # once
cd ~/code/your-project
burro init                                                       # once per project
```

Then open your agent in the project and say "Fill in burro.toml and run the tests with burro". Or run it yourself:

```sh
burro run            # sync and start the default pipeline, returns at once
burro wait           # Ctrl+C is safe, the run keeps going
burro log --step e2e --grep login
```

burro never accepts a host key on its own. If you have never logged in to the server, `host add` tells you to run `ssh` once by hand and check the fingerprint. A server already in `~/.ssh/config` needs only `burro host add myserver <its alias>`.

`burro --help` lists every command on one screen.

## Config

`burro.toml` sits at the repository root. `burro help config` prints the field reference.

```toml
host = "myserver"             # a name from burro host ls
path = "~/burro/myapp"        # project folder on the server, default ~/burro/<repo name>

[pipelines]
default = ["install", "unit"]
verify  = ["install", "unit", "e2e"]

[steps.install]
run = "pnpm install --frozen-lockfile"
env = { NPM_CONFIG_USERCONFIG = "$BURRO_SECRETS/npmrc" }
secrets = ["npmrc"]
skip_if_unchanged = ["pnpm-lock.yaml"]

[steps.unit]
run = "pnpm vitest run --reporter=junit --outputFile=.burro/out/unit.xml"
junit = ".burro/out/unit.xml"
continue_on_fail = true
timeout = "20m"

[steps.e2e]
run = "pnpm playwright test --reporter=junit"
env = { PLAYWRIGHT_JUNIT_OUTPUT_NAME = ".burro/out/e2e.xml" }
junit = ".burro/out/e2e.xml"
timeout = "40m"

[secrets]
npmrc = "~/.npmrc"

[filters]
drop = ['^\s*$']
```

`burro failures` reads JUnit XML. Most test runners write it:

| Stack | Step |
| --- | --- |
| Python | `pytest --junitxml=.burro/out/py.xml` |
| Go | `gotestsum --junitfile .burro/out/go.xml ./...` |
| Rust | `cargo nextest run --profile ci` with a `[profile.ci.junit]` section in `.config/nextest.toml` |
| PHP | `phpunit --log-junit .burro/out/php.xml` |
| Java, Kotlin | Maven and Gradle write JUnit themselves: `junit = "target/surefire-reports/*.xml"` or `"build/test-results/test/*.xml"` |
| Jest | the `jest-junit` reporter with `JEST_JUNIT_OUTPUT_FILE=.burro/out/jest.xml` |
| Vitest | `--reporter=junit --outputFile=.burro/out/unit.xml` |
| Playwright | `--reporter=junit` with `PLAYWRIGHT_JUNIT_OUTPUT_NAME=.burro/out/e2e.xml` |

A step without `junit` gets the clean tail of its log in the summary instead.

## Exit codes

The exit code tells the agent at once whether to fix its code or call you.

| Code | Meaning |
| --- | --- |
| 0 | All steps passed |
| 1 | A step or a test failed |
| 2 | The stand is broken: ssh, sync, missing `rsync`, the runner died, the host key is unknown or changed |
| 3 | `wait` timed out or was interrupted; the run goes on |
| 4 | Bad config or arguments |
| 5 | Another run is active in this project |

Every error is one line plus a `fix:` line with the next step.

## Other install methods

Homebrew, from the tap:

```sh
brew install timhartmann7/tap/burro
```

Since Homebrew 6 a tap outside Homebrew needs your trust before brew loads anything from it. Installing by the full name, as above, trusts only the burro cask. `brew upgrade burro` works from then on.

Go 1.27 or later:

```sh
go install github.com/timhartmann7/burro/cmd/burro@latest
```

A `go install` build carries no server binary. It downloads the one of its own version from GitHub Releases when you add a server, checks it against `checksums.txt`, and only then copies it to the server. The server never needs access to GitHub.

mise and ubi read the release archives directly:

```sh
mise use -g github:timhartmann7/burro
ubi --project timhartmann7/burro --in ~/.local/bin
```

aqua needs a registry entry; the archive names follow the plain pattern, so it has no replacements:

```yaml
packages:
  - type: github_release
    repo_owner: timhartmann7
    repo_name: burro
    asset: burro_{{.OS}}_{{.Arch}}.tar.gz
```

Every release has `checksums.txt`, a keyless cosign signature of it in `checksums.txt.sigstore.json` and a GitHub attestation.

## Docker isolation

Each run has a run key, unique on the host, in `$BURRO_RUN`. Steps get `COMPOSE_PROJECT_NAME=$BURRO_RUN`, so `docker compose` keeps the containers, networks and volumes of every run apart with no change to your project.

A step that starts containers with plain `docker run` must label them:

```sh
docker run --label burro.run=$BURRO_RUN ...
```

At the end of every run, after `burro cancel` and on `burro clean`, burro removes the run's compose project and everything labelled with its key. Nothing else on the server is touched, so a debugging cleanup can't wipe the database of another run.

## Secrets

A secret is a local file named in `[secrets]`. burro sends its contents through the stdin of `ssh`, never in arguments, and the server keeps them in `/dev/shm/burro/<run key>/` for the duration of the run only. Steps find them in `$BURRO_SECRETS`. Nothing is written to the server's disk.

Secret contents are replaced with `***` in step logs, in `exec` output and in every summary. Values shorter than 4 bytes are not masked: masking them would mask half of every log.

## Privacy

burro has no telemetry. Besides `ssh` and `rsync` to the servers you add, it makes these requests:

- Once a day, for a human in a terminal only: `GET https://api.github.com/repos/timhartmann7/burro/releases/latest`, to tell you about a newer version. Never in agent mode. `DO_NOT_TRACK=1`, `BURRO_NO_UPDATE_CHECK=1` or a non-empty `CI` turn it off.
- For a `go install` build only: the server binary and `checksums.txt` from GitHub Releases, when a server needs the binary.
- `install.sh` downloads the archive and `checksums.txt` from GitHub Releases.

The only official repository is `github.com/timhartmann7/burro`.

## License

Apache 2.0, see [LICENSE](LICENSE).
