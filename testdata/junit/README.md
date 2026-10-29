# JUnit fixtures

Each report below came from running the runner for real in a
`debian:stable-slim` container, on a sample project in `/app` with one
passing, one failing, one erroring and one skipped test. The erroring test
calls a library function that throws, so its stack starts outside the
project: in `node_modules`, `site-packages` or the toolchain.

| File | Runner | Command |
| --- | --- | --- |
| `vitest.xml` | vitest 5.0.5, node 20.19.2 | `npx vitest run vitest --reporter=junit --outputFile=out/vitest.xml` |
| `playwright.xml` | @playwright/test 1.64.1, node 20.19.2, projects chromium, firefox and webkit, junit reporter with `includeProjectInTestName: true` | `npx playwright test` |
| `jest.xml` | jest 30.5.2, jest-junit 17.0.0, node 20.19.2 | `JEST_JUNIT_OUTPUT_FILE=out/jest.xml npx jest --rootDir jest --reporters=default --reporters=jest-junit` |
| `pytest.xml` | pytest 8.3.5, python 3.13.5 | `.venv/bin/python -m pytest --junitxml=out/pytest.xml` |
| `gotestsum.xml` | gotestsum 1.8.2, go 1.24.4 | `gotestsum --junitfile out/gotestsum.xml ./...` |
| `nextest.xml` | cargo-nextest 0.9.150, cargo 1.85.1 | `cargo nextest run --profile ci` with `[profile.ci.junit]` in `.config/nextest.toml` |
| `phpunit.xml` | PHPUnit 11.5.19, php 8.4.27 | `phpunit --log-junit out/phpunit.xml tests` |
| `surefire.xml` | maven-surefire-plugin 3.6.1, junit-jupiter 6.1.3, Maven 3.9.9, OpenJDK 21.0.13 | `mvn -B test`, then `target/surefire-reports/TEST-com.example.CalcTest.xml` |

Runner notes:

- nextest leaves `#[ignore]` tests out of its JUnit report, and Rust has no
  skip at run time, so `nextest.xml` has no skipped test. Its erroring test
  returns an `Err`.
- vitest, Jest and nextest report a thrown error as `<failure>`; the others
  use `<error>`.

The `broken-*.xml` files are written by hand: `broken-truncated.xml` is the
first 900 bytes of `jest.xml`, `broken-empty.xml` has no bytes,
`broken-invalid.xml` doesn't close its `<failure>`, and
`broken-wrong-root.xml` has an HTML root.
