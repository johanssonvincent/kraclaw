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

anth=$(grep -E '^[[:space:]]+agentImageAnthropic:' "$FILE" || true)
oai=$(grep -E '^[[:space:]]+agentImageOpenAI:' "$FILE" || true)

fail=0
for line in "$anth" "$oai"; do
    if [[ -z "$line" ]]; then
        continue
    fi
    if [[ ! "$line" =~ @sha256:[0-9a-fA-F]{64} ]]; then
        echo "image not digest-pinned: $line" >&2
        fail=1
    fi
    if [[ "$line" =~ @sha256:0{64} ]]; then
        echo "image uses placeholder digest: $line" >&2
        fail=1
    fi
done
exit "$fail"
