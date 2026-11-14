package gopher

import "testing"

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want Address
	}{
		{"gopher://example.org", Address{Host: "example.org", Port: "70", Type: '1'}},
		{"example.org:7070/0/foo.txt", Address{Host: "example.org", Port: "7070", Type: '0', Selector: "/foo.txt"}},
		{"gopher://example.org/7/v2/vs?hello%20world", Address{Host: "example.org", Port: "70", Type: '7', Selector: "/v2/vs", Query: "hello world"}},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
	}
	if _, err := ParseURL("https://example.org"); err == nil {
		t.Error("expected error for https scheme")
	}
}

func TestParseMenu(t *testing.T) {
	data := []byte("iWelcome\tfake\t(NULL)\t0\r\n" +
		"1Stuff\t/stuff\texample.org\t70\r\n" +
		"hWeb\tURL:https://example.com\texample.org\t70\r\n" +
		"garbage line\r\n" +
		".\r\n" +
		"1After terminator\t/x\texample.org\t70\r\n")
	items := ParseMenu(data)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(items), items)
	}
	if items[0].Type != TypeInfo || items[0].Display != "Welcome" {
		t.Errorf("info line: %+v", items[0])
	}
	if items[1].Type != TypeMenu || items[1].Selector != "/stuff" || items[1].Host != "example.org" {
		t.Errorf("menu line: %+v", items[1])
	}
	if items[2].ExternalURL() != "https://example.com" {
		t.Errorf("url line: %+v", items[2])
	}
	if items[3].Type != TypeInfo || items[3].Display != "garbage line" {
		t.Errorf("malformed line: %+v", items[3])
	}
}

func TestStripTerminator(t *testing.T) {
	got := string(StripTerminator([]byte("hello\r\nworld\r\n.\r\n")))
	if got != "hello\nworld" {
		t.Errorf("got %q", got)
	}
}
