package diff

import "testing"

func TestCanonicalizeUnit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "drops blank lines",
			in:   "[Unit]\n\n\nDescription=foo\n\n",
			want: "[Unit]\nDescription=foo",
		},
		{
			name: "drops hash comments",
			in:   "# leading comment\n[Unit]\n# inline-comment-style\nDescription=foo",
			want: "[Unit]\nDescription=foo",
		},
		{
			name: "drops semicolon comments",
			in:   "; ini-style comment\n[Unit]\nDescription=foo",
			want: "[Unit]\nDescription=foo",
		},
		{
			name: "trims trailing whitespace",
			in:   "[Unit]   \nDescription=foo\t\n",
			want: "[Unit]\nDescription=foo",
		},
		{
			name: "normalizes equals spacing",
			in:   "[Service]\nExecStart = /usr/bin/foo\nEnvironment   =   X=1\n",
			want: "[Service]\nExecStart=/usr/bin/foo\nEnvironment=X=1",
		},
		{
			name: "preserves section header case",
			in:   "[unit]\nDescription=lower\n",
			want: "[unit]\nDescription=lower",
		},
		{
			name: "preserves key order",
			in:   "[Service]\nExecStartPre=/bin/a\nExecStart=/bin/b\nExecStartPost=/bin/c",
			want: "[Service]\nExecStartPre=/bin/a\nExecStart=/bin/b\nExecStartPost=/bin/c",
		},
		{
			name: "preserves section order",
			in:   "[Unit]\nDescription=x\n[Service]\nType=oneshot\n[Install]\nWantedBy=multi-user.target",
			want: "[Unit]\nDescription=x\n[Service]\nType=oneshot\n[Install]\nWantedBy=multi-user.target",
		},
		{
			name: "indented lines lose indent (key=value path)",
			in:   "[Service]\n  ExecStart=/bin/foo\n  Environment=X=1",
			want: "[Service]\nExecStart=/bin/foo\nEnvironment=X=1",
		},
		{
			name: "value with literal equals preserved",
			in:   "[Service]\nEnvironment=KEY=val=ue",
			want: "[Service]\nEnvironment=KEY=val=ue",
		},
		{
			name: "bare line without equals preserved",
			in:   "[Service]\nExecStart=/bin/foo \\\nbar baz",
			want: "[Service]\nExecStart=/bin/foo \\\nbar baz",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CanonicalizeUnit(c.in)
			if got != c.want {
				t.Errorf("CanonicalizeUnit(%q):\n  got:  %q\n  want: %q", c.in, got, c.want)
			}
		})
	}
}

func TestCanonicalizeUnitIdempotent(t *testing.T) {
	// Canonicalization must be a fixed-point: canonicalizing canonical output
	// produces the same bytes. Otherwise hashes diverge between rounds.
	inputs := []string{
		"[Unit]\nDescription=foo",
		"[Service]\nExecStart=/bin/sh -c 'echo hi'\nEnvironment=X=1\n",
		"# comment\n[Unit]\n  Description = test  \n\n[Service]\nType=simple\n",
	}
	for _, in := range inputs {
		once := CanonicalizeUnit(in)
		twice := CanonicalizeUnit(once)
		if once != twice {
			t.Errorf("not idempotent for input %q:\n  once:  %q\n  twice: %q", in, once, twice)
		}
	}
}

func TestCanonicalizeUnitDistinguishesBehavior(t *testing.T) {
	// Two unit files that mean different things to systemd must NOT
	// canonicalize to the same bytes. Spot-check the cases the spec calls
	// out as behavior-significant.
	cases := []struct {
		name   string
		a, b   string
		shared bool // true if the canonicalization should match
	}{
		{
			name:   "key reordering changes behavior",
			a:      "[Service]\nExecStart=/bin/a\nExecStart=/bin/b",
			b:      "[Service]\nExecStart=/bin/b\nExecStart=/bin/a",
			shared: false,
		},
		{
			name:   "case-different section is different",
			a:      "[Service]\nType=oneshot",
			b:      "[service]\nType=oneshot",
			shared: false,
		},
		{
			name:   "whitespace-only diff is the same",
			a:      "[Service]\nType=oneshot\n",
			b:      "[Service]\n  Type =  oneshot  \n\n",
			shared: true,
		},
		{
			name:   "comment-only diff is the same",
			a:      "[Service]\nType=oneshot",
			b:      "# header comment\n[Service]\n# inline\nType=oneshot",
			shared: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ca := CanonicalizeUnit(c.a)
			cb := CanonicalizeUnit(c.b)
			same := ca == cb
			if same != c.shared {
				t.Errorf("CanonicalizeUnit equality mismatch: shared=%v, want %v\n  a: %q\n  b: %q",
					same, c.shared, ca, cb)
			}
		})
	}
}
