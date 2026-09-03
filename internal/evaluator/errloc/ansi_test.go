package errloc

import "testing"

// The three shapes vitest actually emitted in run api-72dad6bb281cacee338f43c48432a780, verbatim
// from the audit log's fixer error_output, plus an OSC hyperlink of the kind newer runners print.
func TestStripANSI(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"dim_between_colon_and_line": {
			in:   "\x1b[36m \x1b[2m❯\x1b[22m src/app/router.test.tsx:\x1b[2m59:24\x1b[22m\x1b[39m",
			want: " ❯ src/app/router.test.tsx:59:24",
		},
		"coloured_pass_tick": {
			in:   " \x1b[32m✓\x1b[39m src/app/AppLayout.test.tsx \x1b[2m(\x1b[22m\x1b[2m4 tests\x1b[22m\x1b[2m)\x1b[22m",
			want: " ✓ src/app/AppLayout.test.tsx (4 tests)",
		},
		"bold_reverse_fail_marker": {
			in:   "\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m src/pages/OrdersPage.test.tsx\x1b[2m > \x1b[22mOrdersPage",
			want: " FAIL  src/pages/OrdersPage.test.tsx > OrdersPage",
		},
		"osc_hyperlink_bel_and_st": {
			in:   "see \x1b]8;;https://vitest.dev\x07docs\x1b]8;;\x1b\\ now",
			want: "see docs now",
		},
		"private_mode_and_cursor_csi": {
			in:   "\x1b[?25la\x1b[2K\x1b[1Gb\x1b[?25h",
			want: "ab",
		},
		"plain_text_untouched": {
			in:   "src/lib/validation.test.ts:27:38 AssertionError: expected 3 to be null",
			want: "src/lib/validation.test.ts:27:38 AssertionError: expected 3 to be null",
		},
		"empty": {in: "", want: ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := StripANSI(c.in)
			if got != c.want {
				t.Fatalf("StripANSI(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
			if again := StripANSI(got); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}
