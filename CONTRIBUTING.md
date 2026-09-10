# Contributing to CLAN

Use plain English in issues, commits and documentation. Follow the
[development guide](docs/development.md) for code and tests, the
[ADRs](README.md#decision-log) for product behavior, and the
[Code of Conduct](CODE_OF_CONDUCT.md) when working with others.

## Issues

Check for an existing issue before opening one. Give each issue one clear outcome
and a short title that describes the problem or intended change.

Use these sections:

- **Problem:** explain what happens today and what is missing or wrong. For a bug,
  include reproduction steps, expected and actual behavior, and relevant versions.
  Reference files or contracts when they help explain the problem.
- **Why it matters:** describe the effect on users or development. Explain why
  the work is useful without repeating the problem.
- **Done when:** list observable results that will make the task complete,
  including the tests or checks needed to verify them.

Keep the issue self-contained. Add examples, relevant links and scope limits when
useful. Separate agreed behavior from proposals; do not invent implementation
details to fill out the description. Keep credentials and private request content
out of examples and logs.

## Branches

For issue work, branch from `main` and use only the issue number as the branch name.
For issue #1:

```sh
git switch main
git switch -c 1
```

Use `1`, not `issue-1`, `feature/1` or a descriptive suffix. Keep the branch focused
on its issue.

## Commits

For issue work, use one line in this format:

```text
#<issue number>: <change>
```

Examples:

```text
#1: implement round-robin account selection
#1: test selection when an account is removed
```

Use a short English description that starts with an action, such as `add`, `fix`
or `test`. Describe the change, not the work session. Put longer explanations in
the issue or pull request.

Do not add a commit body, co-author lines, sign-off trailers or generated-by
messages. Git still records the normal author and committer metadata.
Keep unrelated changes out of the commit and inspect the staged diff before
committing.

## Checks and review

Use the commands in the [Justfile](Justfile): `just format` formats Go code,
`just format --check` checks formatting without changing files, `just deps` updates
and verifies dependencies, and `just lint` runs `go vet ./...`. `just test-unit`
runs fast tests, and `just test` runs all tests. Run the relevant checks before
submitting changes.
Documentation-only changes need link and formatting checks, not Go tests.

The [CI workflow](.github/workflows/ci.yml) runs formatting, dependency, static
analysis and test checks on pull requests to `main` and pushes to `main`.
Use the Go version in `go.mod` and the Just version pinned in the workflow.
Golangci-lint and Docker lifecycle commands are not configured yet.

A pull request should link its issue, explain the resulting behavior, and state
which checks passed or could not run. Update affected documentation with the code.
