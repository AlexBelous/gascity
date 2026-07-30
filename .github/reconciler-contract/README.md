# Reconciler contract anchor

`RATIFIED_V1.json` pins the immutable Git objects that define the reconciler
contract. It is intentionally small: it supplies a base-owned reference for
the trusted verifier without copying or executing candidate code.

The `Reconciler contract` workflow runs only from `pull_request_target` base
configuration. It checks out trusted base code, fetches the fixed candidate
and ratification objects, and uses the GitHub API to collect every filename
changed by the triggering pull request. Only that JSON filename list crosses
into the trusted checkout. The workflow never checks out or executes candidate
code. The trusted tagged docsync package rejects changes to any path listed in
the anchor.

This repository anchor does not configure GitHub branch protection or a
required-workflow ruleset. An administrator must require this exact workflow,
with no bypass, and validate it with a seeded protected-path mutation pull
request before this anchor can claim merge blocking.
