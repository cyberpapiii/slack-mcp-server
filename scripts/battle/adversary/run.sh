#!/bin/sh
set -u

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
out="$repo/testdata/battle/adversary"
provider_tmp="$repo/pkg/provider/adv001_generated_test.go"
server_tmp="$repo/pkg/server/adv001_generated_test.go"
contracts="$repo/testdata/battle/contracts"
backup=$(mktemp -d "${TMPDIR:-/tmp}/adv001.XXXXXX")
failures=0

cleanup() {
	rm -f "$provider_tmp" "$server_tmp"
	for name in tool-registration-surface.txt message-csv-legend.txt error-taxonomy.json; do
		if [ -f "$backup/$name" ]; then
			cp "$backup/$name" "$contracts/$name"
		fi
	done
	rm -rf "$backup"
}
trap cleanup EXIT INT TERM

mkdir -p "$out"
for name in tool-registration-surface.txt message-csv-legend.txt error-taxonomy.json; do
	cp "$contracts/$name" "$backup/$name"
done

if [ -e "$provider_tmp" ] || [ -e "$server_tmp" ]; then
	echo "ADV-001 temporary test path already exists" >&2
	exit 2
fi
cp "$repo/scripts/battle/adversary/provider_adversary_test.go.in" "$provider_tmp"
cp "$repo/scripts/battle/adversary/server_adversary_test.go.in" "$server_tmp"
gofmt -w "$provider_tmp" "$server_tmp"

run_case() {
	name=$1
	shift
	log="$out/$name.log"
	(
		cd "$repo" || exit 1
		"$@"
	) >"$log" 2>&1
	status=$?
	printf '%s exit=%s\n' "$name" "$status"
	if [ "$status" -ne 0 ]; then
		failures=$((failures + 1))
	fi
}

expect_contract_failure() {
	name=$1
	pattern=$2
	shift 2
	log="$out/$name.log"
	(
		cd "$repo" || exit 1
		"$@"
	) >"$log" 2>&1
	status=$?
	printf '%s exit=%s expected=nonzero\n' "$name" "$status"
	if [ "$status" -eq 0 ] || ! rg -q "$pattern" "$log"; then
		failures=$((failures + 1))
	fi
}

run_case provider-races go test -race -count=5 -v -run '^TestADV00[123]' ./pkg/provider
run_case server-tool-paths go test -race -count=5 -v -run '^TestADV001' ./pkg/server

awk 'NR == 1 { first = $0; next } NR == 2 { print; print first; next } { print }' \
	"$contracts/tool-registration-surface.txt" >"$backup/reordered.txt"
cp "$backup/reordered.txt" "$contracts/tool-registration-surface.txt"
expect_contract_failure fixture-tool-reorder 'contract drift' \
	env UPDATE_BATTLE_GOLDENS=0 go test -count=1 -run '^TestBattleContractToolRegistrationSurface$' ./pkg/server
cp "$backup/tool-registration-surface.txt" "$contracts/tool-registration-surface.txt"

printf '\n# ADV-001 perturbation\n' >>"$contracts/message-csv-legend.txt"
expect_contract_failure fixture-message-perturb 'contract drift' \
	env UPDATE_BATTLE_GOLDENS=0 go test -count=1 -run '^TestBattleContractMessageCSVLegend$' ./pkg/handler
cp "$backup/message-csv-legend.txt" "$contracts/message-csv-legend.txt"

awk '{ gsub(/"is_error": true/, "\"is_error\": false"); print }' \
	"$contracts/error-taxonomy.json" >"$backup/error-perturbed.json"
cp "$backup/error-perturbed.json" "$contracts/error-taxonomy.json"
expect_contract_failure fixture-error-perturb 'contract drift' \
	env UPDATE_BATTLE_GOLDENS=0 go test -count=1 -run '^TestBattleContractErrorTaxonomy$' ./pkg/server
cp "$backup/error-taxonomy.json" "$contracts/error-taxonomy.json"

cp "$backup/reordered.txt" "$contracts/tool-registration-surface.txt"
run_case golden-update-autoaccept env UPDATE_BATTLE_GOLDENS=1 \
	go test -count=1 -run '^TestBattleContractToolRegistrationSurface$' ./pkg/server
if ! cmp -s "$backup/tool-registration-surface.txt" "$contracts/tool-registration-surface.txt"; then
	echo "golden-update-autoaccept did not restore canonical fixture" >>"$out/golden-update-autoaccept.log"
	failures=$((failures + 1))
else
	echo "perturbed fixture overwritten with current output without review prompt" \
		>>"$out/golden-update-autoaccept.log"
fi

cleanup
trap - EXIT INT TERM

if [ "$failures" -ne 0 ]; then
	printf 'ADV-001 scenario failures=%s\n' "$failures" >&2
	exit 1
fi
echo "ADV-001 scenarios complete; expected fixture failures observed"
