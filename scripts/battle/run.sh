#!/bin/sh
set -eu

update=0
if [ "${1:-}" = "--update" ]; then
	update=1
elif [ "$#" -ne 0 ]; then
	echo "usage: $0 [--update]" >&2
	exit 2
fi

UPDATE_BATTLE_GOLDENS="$update" \
	go test -count=1 -run '^TestBattleContract' ./pkg/server ./pkg/handler

go test -run '^$' -bench '^BenchmarkBattle' -benchmem -count=5 \
	./pkg/server ./pkg/handler ./pkg/text
