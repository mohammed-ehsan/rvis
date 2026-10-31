package units

import "testing"

func TestFormat(t *testing.T) {
	cases := []struct{ got, want string }{
		{CPU(0), "0"},
		{CPU(250), "250m"},
		{CPU(1000), "1"},
		{CPU(1500), "1.5"},
		{CPU(2333), "2.33"},
		{Cores(9580), "9.58"},
		{Bytes(0), "0"},
		{Bytes(512), "512"},
		{Bytes(128 << 20), "128Mi"},
		{Bytes(1536 << 20), "1.5Gi"},
		{Bytes(13 << 30), "13Gi"},
		{Bytes(150 << 30), "150Gi"},
		{DecimalBytes(3_000_000_000), "3G"},
		{DecimalBytes(2_500_000_000), "2.5G"},
		{DecimalBytes(500_000_000), "500M"},
		{DecimalBytes(128_000), "128k"},
		{Bytes(3_000_000_000), "2.8Gi"},
		{Grouped(3_000_000_000), "3,000,000,000"},
		{Grouped(-1234), "-1,234"},
		{Grouped(999), "999"},
		{Plain(2000), "2"},
		{Plain(500), "0.5"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q, want %q", i, c.got, c.want)
		}
	}
}
