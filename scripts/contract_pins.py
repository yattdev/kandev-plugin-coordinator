"""Single, independently-pinned source of truth for the vendored Coordinator
policy contract's provenance.

These three constants are the only trusted anchor for "which immutable
source did this vendored tree come from". They are deliberately NOT read
back from `docs/contracts/upstream-manifest.json` (which is generated data,
and therefore something an attacker or a broken refresh could edit or
substitute) and NOT accepted as free-form input on the command line.

Both `scripts/verify_contract_provenance.py` (independently re-checks the
manifest's provenance block against these constants) and
`scripts/generate_contract_manifest.py` (refuses to write a manifest whose
requested source_repository/source_branch/source_commit disagree with these
constants) import this module. Update these three values ONLY as step one of
a deliberate vendor refresh -- see docs/contracts/PROVENANCE.md.
"""

EXPECTED_SOURCE_REPOSITORY = "yattdev/tasks-coordinator"
EXPECTED_SOURCE_BRANCH = "feature/codify-coordinator-p-8eu"
EXPECTED_SOURCE_COMMIT = "2ca27d00477dc298fc91187274968f1fc3970fef"

_HEX_DIGITS = set("0123456789abcdef")


def is_full_commit_sha(value):
    """True only for a well-formed, full (40 hex char) git commit SHA.

    Rejects short SHAs, branch/tag names such as "main" or "HEAD", empty
    strings, and any non-string value. A full-length SHA is required so a
    mutable ref (a branch name, or a short/abbreviated SHA that could resolve
    to different commits over time) can never satisfy provenance checks.
    """
    if not isinstance(value, str) or len(value) != 40:
        return False
    return set(value.lower()) <= _HEX_DIGITS


def matches_pinned_commit(value):
    """True only if value is a full SHA and equals the pinned commit."""
    return is_full_commit_sha(value) and value.lower() == EXPECTED_SOURCE_COMMIT.lower()
