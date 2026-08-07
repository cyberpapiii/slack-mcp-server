package text

import (
	"strings"
	"testing"
)

func BenchmarkBattleProcessText(b *testing.B) {
	input := strings.Repeat(
		"Status <https://example.com/build/42|build 42> for <@U0123456789> is ready. ",
		20,
	)

	b.ReportAllocs()
	for b.Loop() {
		if got := ProcessText(input); got == "" {
			b.Fatal("empty result")
		}
	}
}
