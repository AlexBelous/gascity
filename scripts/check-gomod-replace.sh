#!/usr/bin/env bash
# check-gomod-replace.sh [go.mod-path]
#
# Rejects every replace except the exact beads v1.3.0 SHOW fork approved for
# this integration. Public releases still reject all replace directives.
set -euo pipefail

gomod="${1:-go.mod}"

if [[ ! -f "$gomod" ]]; then
	echo "check-gomod-replace: $gomod not found" >&2
	exit 1
fi

approved_body="github.com/steveyegge/beads => github.com/AlexBelous/beads v1.1.1-0.20260928222722-da08f27390f1"
replace_count=0
in_replace_block=0

reject() {
	echo "check-gomod-replace: BLOCKED — only the exact beads v1.3.0 SHOW fork replacement is approved: $1" >&2
	return 1
}

while IFS= read -r line || [[ -n "$line" ]]; do
	stripped="${line#"${line%%[![:space:]]*}"}"
	stripped="${stripped%"${stripped##*[![:space:]]}"}"
	if [[ "$stripped" == "replace (" && "$in_replace_block" -eq 0 ]]; then
		in_replace_block=1
		continue
	fi
	if [[ "$in_replace_block" -eq 1 ]]; then
		if [[ "$stripped" == ")" ]]; then
			in_replace_block=0
			continue
		fi
		[[ -z "$stripped" || "$stripped" == //* ]] && continue
		[[ "$stripped" == "$approved_body" ]] || reject "$stripped"
		replace_count=$((replace_count + 1))
		continue
	fi
	if [[ "$stripped" == replace* ]]; then
		[[ "$stripped" == "replace $approved_body" ]] || reject "$stripped"
		replace_count=$((replace_count + 1))
	fi
done < "$gomod"

[[ "$in_replace_block" -eq 0 ]] || reject "unterminated replace block"
[[ "$replace_count" -le 1 ]] || reject "duplicate replacement"

if [[ "$replace_count" -eq 1 ]]; then
	beads_require_count=$(grep -E '^[[:space:]]*(require[[:space:]]+)?github[.]com/steveyegge/beads[[:space:]]+' "$gomod" | grep -vc '=>' || true)
	approved_require_count=$(grep -Ec '^[[:space:]]*(require[[:space:]]+)?github[.]com/steveyegge/beads[[:space:]]+v1[.]3[.]0([[:space:]]*(//.*)?)?$' "$gomod" || true)
	[[ "$beads_require_count" -eq 1 && "$approved_require_count" -eq 1 ]] || reject "beads requirement is not exactly v1.3.0"
fi

echo "check-gomod-replace: OK (no unapproved replace directives)"
