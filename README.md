# mazama

A terminal client for the small web: **gopher**, **gemini** and **usenet**, built with
[bubbletea](https://github.com/charmbracelet/bubbletea),
[lipgloss](https://github.com/charmbracelet/lipgloss) and
[glamour](https://github.com/charmbracelet/glamour). I literally vibecoded
this in an hour.

| gemini | gopher |
| --- | --- |
| ![mazama showing a gemini capsule](docs/screenshot-gemini.png) | ![mazama showing a gopher menu](docs/screenshot-gopher.png) |

## Run

```sh
nix run github:Flonk/mazama -- [address]   # with nix
make run ARGS="[address]"                  # with Go ≥ 1.26
```

`address` can be a `gopher://`, `gemini://`, `news://`, `nntps://` or
`https://` URL, or just a host (`sdf.org`, `example.org/notes/a.gmi`,
`news.eternal-september.org`): without a scheme, mazama guesses the protocol
from the address and, if needed, by probing the host. It defaults to
`gopher://gopher.floodgap.com/`.

## Features

- Gopher menus with type tags, search items, and `URL:` links.
- Gemini with trust-on-first-use certificates
  (`~/.local/share/mazama/known_hosts`), input prompts and redirects.
- Gemtext and markdown rendered with glamour. Links show only their text:
  click once to see the URL, again to open it.
- Usenet (reading only):
  - `news://server/` lists the groups; big hierarchies fold into `comp.*`-style
    entries, so a server with tens of thousands of groups stays browsable.
    "search groups" finds groups by name, or by a wildmat such as `comp.lang.*`.
  - Group pages list the newest 200 articles, newest first, with an
    "older articles" entry at the end.
  - Articles show the subject, author, date, and links to their groups, the
    message they reply to, and the start of the thread. `>` quoting becomes
    nested quote bars, "… wrote:" lines are set in italics, and the signature
    goes below a rule. Line breaks and indentation are kept, and URLs are
    clickable. MIME encodings and charsets are decoded, and articles posted as
    `text/markdown` are rendered as markdown.
  - Addresses: `news://server/comp.lang.go`, `news://server/<message-id>`,
    `nntp://server/group/123`.
  - On a news page, press `n` and type a group name such as
    `sci.astro.amateur` to go there on the same server; a name that isn't a
    group becomes a search. A bare group name typed at `:` works too.
  - Encryption: `nntps://` (port 563) uses TLS from the start. `news://`
    (port 119) switches to TLS with STARTTLS when the server offers it.
    Certificates are checked against the system roots, and mazama never sends a
    password over an unencrypted connection.
  - Logins: when a server requires one, or when you pick "log in" on a group
    list (some servers, like Eternal September, show a few groups until you
    log in), mazama asks for it. Once the server accepts it, mazama offers to
    save it in the system keyring (gnome-keyring, KWallet, macOS Keychain),
    one entry per server. Logins in `~/.authinfo` or `~/.netrc`
    (`machine news.eternal-september.org login USER password PASS`) work too.
- Web links open in your browser via `xdg-open` / `open`.
- Mouse support: wheel, click to follow, back button.
- Themes: `-style one-dark` (default), any glamour style (`dracula`,
  `tokyo-night`, …), `auto`, a glamour JSON file, or a base16 scheme as JSON
  (`{"base00": "#282c34", …}`). Menus follow the theme. Without `-style`,
  mazama uses `$MAZAMA_STYLE`, then `~/.config/mazama/style.json` if present.

## Keys

| key               | action                             |
| ----------------- | ---------------------------------- |
| ↑↓ / j k          | move / scroll                      |
| ctrl+↑↓           | move / scroll 10 lines             |
| tab / shift+tab   | next / previous link               |
| enter             | open                               |
| backspace         | back                               |
| `:`               | go to address                      |
| n                 | go to a usenet group (news pages)  |
| r                 | reload                             |
| esc               | cancel / deselect                  |
| q                 | quit                               |

## Command line (scripts and agents)

Everything the TUI does is also a subcommand. Pages are printed as plain
text with numbered links (`[n]` at the start of a menu line, or after a
link's text in a document), followed by the list of link URLs; add `--json`
for structured output (`url`, `kind`, `text`, `links[]` with `n`, `label`,
`url`, `action`, and `input` for prompts).

```sh
mazama open gemini://geminiprotocol.net/    # :  go to an address (protocol guessed for bare hosts)
mazama follow 3                             # ⏎  follow link 3 on the current page
mazama follow 7 -q "search terms"           #    follow a search link and answer it
mazama input "search terms"                 #    answer the current page's prompt (gopher search, gemini 1x)
mazama input --stdin < secret.txt           #    ...sensitive input (gemini 11) via stdin
mazama back                                 # ⌫  previous page (from the session, not refetched)
mazama reload                               # r
mazama group comp.lang.go                   # n  usenet group on the current server (or a search)
mazama show | links | history               #    the current page, its links, the history
echo PASS | mazama login news.example.org --user me   # news login, checked then saved in the keyring
mazama get <address> [-q text]              #    one-off fetch that leaves the session alone
```

Commands share a session (current page and history) stored in
`~/.local/state/mazama/sessions/`; `--session NAME` or `$MAZAMA_SESSION`
keeps separate agents apart. Link actions are `open`, `query` (needs input:
use `-q` or `input`), `login`, `external` (web links: printed, or opened with
`follow N --browser`) and `none` (unsupported item types). Exit codes: 0 ok,
1 error, 2 usage, 3 news login required. `mazama help` lists everything.

## Install

```sh
nix profile install github:Flonk/mazama     # nix
go install github.com/Flonk/mazama@latest  # go
make install                               # from a checkout; PREFIX=/usr/local by default
```

### Home Manager and Stylix

The flake provides a Home Manager module. With [Stylix](https://github.com/nix-community/stylix)
enabled, mazama picks up its base16 palette automatically:

```nix
# flake inputs: mazama.url = "github:Flonk/mazama";
imports = [ inputs.mazama.homeManagerModules.default ];
programs.mazama.enable = true;
# stylix.targets.mazama.enable = false;  # opt out of Stylix theming
# programs.mazama.style = "dracula";     # or pick a style yourself
```

For development: `nix develop` (or `direnv allow`) for a shell with Go, then
`make test`.

## License

MIT
