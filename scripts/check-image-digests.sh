#!/usr/bin/env bash
set -euo pipefail

# Verifies that production agent images are digest-pinned (@sha256:<64-hex>) and
# not placeholder all-zero digests. Used as a prepush / CI guard so warm-node
# ImagePullPolicy=IfNotPresent never silently drifts to a moving :latest tag.

FILE="${1:-helm/values-prod.yaml}"
if [[ ! -f "$FILE" ]]; then
    echo "missing $FILE" >&2
    exit 1
fi

# Keys that MUST be present and digest-pinned in production. A missing required
# key is a failure: a renamed/dropped key would otherwise leave prod unpinned
# while the guard stays green.
required_keys=(agentImageAnthropic agentImageOpenAI)
# Keys validated only if present (e.g. the sandbox default image).
optional_keys=(agentImage)

fail=0

# check_line validates a single "key: value" line is digest-pinned.
check_line() {
    local line="$1"
    if [[ ! "$line" =~ @sha256:[0-9a-fA-F]{64} ]]; then
        echo "image not digest-pinned: $line" >&2
        fail=1
    fi
    if [[ "$line" =~ @sha256:0{64} ]]; then
        echo "image uses placeholder digest: $line" >&2
        fail=1
    fi
}

# validate_key checks every occurrence of a key individually (so a duplicate
# unpinned entry cannot hide behind a pinned one) and sets the global `found`
# to 1 if the key appears at least once. Uses process substitution (not a pipe)
# so check_line's mutations of `fail` persist in the current shell.
found=0
validate_key() {
    local key="$1" line
    found=0
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        found=1
        check_line "$line"
    done < <(grep -E "^[[:space:]]+${key}:" "$FILE" || true)
}

for key in "${required_keys[@]}"; do
    validate_key "$key"
    if [[ "$found" -eq 0 ]]; then
        echo "required image key missing: $key (in $FILE)" >&2
        fail=1
    fi
done

for key in "${optional_keys[@]}"; do
    validate_key "$key"
done

exit "$fail"
