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
