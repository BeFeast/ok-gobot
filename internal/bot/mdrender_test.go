package bot

import "testing"

func TestRenderTelegramHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"## Результат", "<b>Результат</b>"},
		{"**bold** text", "<b>bold</b> text"},
		{"- item", "• item"},
		{"a `code_x` b", "a <code>code_x</code> b"},
		{"[link](https://x.com)", `<a href="https://x.com">link</a>`},
		{"5 < 6 & 7 > 2", "5 &lt; 6 &amp; 7 &gt; 2"},
		{"**a_b** and `**not bold**`", "<b>a_b</b> and <code>**not bold**</code>"},
	}
	for _, c := range cases {
		if got := renderTelegramHTML(c.in); got != c.want {
			t.Errorf("renderTelegramHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
