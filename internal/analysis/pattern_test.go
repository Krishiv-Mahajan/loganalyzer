package analysis

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain message", "plain message"},
		{"connection to 10.0.0.1:5432 timed out after 30s", "connection to <ip>:<n> timed out after <n>s"},
		{"request 550e8400-e29b-41d4-a716-446655440000 failed", "request <uuid> failed"},
		{"segfault at 0x7ffe3a2b", "segfault at <hex>"},
		{"trace id 4bf92f3577b34da6a3ce929d0e0e4736 dropped", "trace id <hex> dropped"},
		{`user "alice" not found`, "user <str> not found"},
		{"user 'bob' not found", "user <str> not found"},
		{"can't connect, don't retry", "can't connect, don't retry"},
		{"took 12.5ms with 3 retries", "took <n>ms with <n> retries"},
		{"deadbeefcafe is not an id", "deadbeefcafe is not an id"},
		{"  spaced\tout   message ", "spaced out message"},
		{"order 12345678 rejected", "order <n> rejected"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeGroupsVariants(t *testing.T) {
	a := Normalize("payment 1001 failed for user 42: card declined")
	b := Normalize("payment 2002 failed for user 7: card declined")
	if a != b {
		t.Errorf("variants normalized differently: %q vs %q", a, b)
	}
	c := Normalize("payment 1001 failed for user 42: insufficient funds")
	if a == c {
		t.Errorf("distinct failures collapsed into one pattern: %q", a)
	}
}

func BenchmarkNormalize(b *testing.B) {
	msgs := []string{
		"database connection to 10.0.0.5:5432 failed after 3 retries",
		"request 550e8400-e29b-41d4-a716-446655440000 timed out",
		"cache miss",
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = Normalize(msgs[i%len(msgs)])
		i++
	}
}
