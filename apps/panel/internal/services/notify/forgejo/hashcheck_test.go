package forgejo

import "testing"

// TestMessageHashMatchesPythonReference locks the hash format to the tracer
// reference implementation (sha256 → big-int → base62, 10 least-significant
// digits) so existing issues keep their hash across upgrades.
func TestMessageHashMatchesPythonReference(t *testing.T) {
	cases := map[string]string{
		"The quick brown fox jumps over the lazy dog": "cXR3KD2Qys",
		"connection refused":                          "SUwVWRnIbV",
		"hello world":                                 "JCGsqvYgeM",
		"":                                            "5iXlhPEewJ",
		"dependency.failed: upstream postgres unreachable after 3 retries": "9cAifPqSSN",
	}
	for msg, want := range cases {
		if got := messageHash(msg); got != want {
			t.Errorf("messageHash(%q) = %q, want %q", msg, got, want)
		}
	}
}
