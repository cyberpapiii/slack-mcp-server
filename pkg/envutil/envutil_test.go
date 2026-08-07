package envutil

import "testing"

func TestIsTruthy(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"  yes  ", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"maybe", false},
	}
	for _, tc := range cases {
		if got := IsTruthy(tc.in); got != tc.want {
			t.Fatalf("IsTruthy(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}
