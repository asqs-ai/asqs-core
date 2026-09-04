package config

import "testing"

func TestOllamaKeepAliveJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "  ", want: ""},
		{in: "-1", want: "-1"},
		{in: "0", want: "0"},
		{in: "600", want: "600"},
		{in: "30m", want: `"30m"`},
		{in: "1h30m", want: `"1h30m"`},
		{in: "forever", wantErr: true},
		{in: "30 minutes", wantErr: true},
		{in: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		got, err := OllamaKeepAliveJSON(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error, got %s", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%q: got %s, want %s", tc.in, got, tc.want)
		}
	}
}
