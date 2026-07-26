package cid

import "testing"

// Ground truth generated with:
//
//	ipfs add --cid-version=1 --raw-leaves --hash=sha2-256 --only-hash -Q <file>
func TestFromCanonical_KnownVectors(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{"empty", []byte(""), "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"},
		{"hello", []byte("hello"), "bafkreibm6jg3ux5qumhcn2b3flc3tyu6dmlb4xa7u5bf44yegnrjhc4yeq"},
		{"json", []byte(`{"a":1,"b":2}`), "bafkreicdewgp66b744bw3csdam7ygcw7yyhmanzyerzvjcwhik4iqkjho4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := FromCanonical(c.input)
			if got != c.want {
				t.Errorf("FromCanonical(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestFromDigest_MatchesFromCanonical(t *testing.T) {
	input := []byte(`{"x":1}`)
	cidStr, digest := FromCanonical(input)
	if got := FromDigest(digest); got != cidStr {
		t.Errorf("FromDigest(digest) = %q, want %q", got, cidStr)
	}
}
