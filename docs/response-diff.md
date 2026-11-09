# Comparing responses

HTTP-40: any two
of a request's responses, the latest send, an earlier response (HTTP-39)
or a saved example (HTTP-38), side by side, with the differences in the
status line, the header fields and the body marked.

| Piece | Where |
|---|---|
| The comparison: headers, JSON structure, line diff, limits | `internal/core/respdiff` (standard library only) |
| The bound method | `HTTPService.CompareResponses` (`internal/app/services/http/compare.go`) |
| Reading the two responses, the select values | `commands/responseCompare.ts` |
| The dialog | `components/response/CompareResponsesDialog.tsx`, opened by **Compare…** in the response pane's source bar (`ResponseSourceBar.tsx`) |
| Folding unchanged lines | `foldRows` in `lib/responseDiff.ts` |

The diff runs in Go: the bodies are already there as the history and
the examples keep them, JSON is read with exact numbers and member order
(the browser's `JSON.parse` would reorder integer-like keys and round big
numbers), and the core package is tested like the rest of the engine. No
npm package is used; the Myers diff is written out in `lines.go`.

## What is compared

Both responses are **stored responses** (`StoredResponseInputDTO`, as for
the response view, [http-client.md](http-client.md)): the latest send and
an earlier one are read from their history entries (so the latest is
offered only when it was recorded, `historyId`), an example as the
request file keeps it. Bodies are therefore what the history keeps, at
most 64 KiB each (`bodyClipped`), and masked.

**Status.** The two status lines (`200 OK`), changed or not.

**Headers.** By name, letter case ignored (`ETag` and `etag` are one
name). A name's values are the values of its fields in order, so a
second `Set-Cookie` is a change. Each name is `same`, `added` (only on the
right), `removed` (only on the left) or `changed`. Names come in the left
response's order, then those only the right one has. Counts:
`headersAdded`, `headersRemoved`, `headersChanged`.

**Body.** One of these modes:

| Mode | When | Compared |
|---|---|---|
| `json` | both bodies are valid JSON | by structure, below |
| `text` | both are UTF-8 text, not both JSON | line by line (Myers' shortest edit script, after the common first and last lines are set aside); a run of removed and added lines between two same ones is paired as changed lines, the rest removed or added. A trailing line break does not count as a line, and CR before LF is ignored |
| `binary` | a body is not UTF-8 | only equal or not, with the offset of the first byte that differs |
| `too_large` | a body is over 1 MiB | only equal or not |
| `empty` | both are empty | |

### JSON by structure

The values are compared **by path**, not by text, so formatting and
member order do not count, and `1.0` equals `1` (numbers are compared
exactly by value, with `big.Rat`).

- **Objects** by member name. A name used twice is paired by occurrence
  (the second `k` with the second `k`). A member only on one side is
  `added` or `removed` with its whole value.
- **Arrays by index**: element 0 with element 0, and so on; elements past
  the shorter array are added or removed. An element inserted in the
  middle therefore shows every later element as changed. That is said in
  a notice whenever an array differs, as the price of a simple, exact
  rule (matching elements by content would guess).
- **Values** of another type (`"42"` and `42`, `null` and `[]`) are
  `changed`, with both types; scalars of the same type are compared by
  value.
- **Changes** (`body.changes`) are listed by path in normalized JSONPath
  ([jsonpath.md](jsonpath.md)): `$.user.age`, `$.items[2]`,
  `$['odd key']`, each `added`, `removed` or `changed` with the values as
  compact JSON (at most 200 characters). A change covers its whole
  subtree: an added object is one change. `added`, `removed` and
  `changed` count them.

**Rows.** Each side is shown **pretty-printed** (two spaces, members as
the body has them), lined up by path: a row is a line of the left, of the
right, or of both. Unchanged values are `same` rows, a changed value's
lines are `changed` rows side by side (the longer side's extra lines face
nothing), an added or removed value's lines face nothing. Each line keeps
its number on its side and each row the path it belongs to. Members the
right body has in another order are shown in the left body's order (a
`respdiff.key_order` notice says so); a member only the right one has
comes after the member both have that precedes it there. Everything else
of each side reads as its own pretty-printed text.

### Limits

Every limit that cuts a comparison short says so in `body.notices`
(`code`, `message`):

| Limit | Default | Beyond it |
|---|---|---|
| body size | 1 MiB (`MaxBodyBytes`) | `respdiff.too_large`: equal or not only |
| JSON values, nesting | 200,000 values, 200 levels | `respdiff.json_limits`: compared as text |
| lines of one text | 20,000 (`MaxLines`) | `respdiff.too_many_lines`: the first 20,000 are compared |
| lines added and removed | 1,000 (`MaxEdits`) | `respdiff.too_many_changes`: the differing lines are shown all removed, then all added (the edit search stops; its memory is bounded by the square of the limit) |
| rows | 20,000 (`MaxRows`) | `respdiff.rows_limited`: the rest are left out |
| JSON changes listed | 5,000 (`MaxChanges`) | `respdiff.changes_limited`; the counts stay exact |

Other notices: `respdiff.clipped` (a body was kept only in part: the
rest is not compared, and a clipped JSON body is compared as text),
`respdiff.not_json` (one body is JSON and the other is not),
`respdiff.binary`, `respdiff.arrays_by_index`, `respdiff.key_order`.
Comparing two 64 KiB JSON bodies of 1,500 objects takes about 6 ms
(`BenchmarkJSON64KiB`).

## Service

`HTTPService.CompareResponses({left, right})`, two
`StoredResponseInputDTO`s, returns `ResponseDiffDTO`:

- `status`: `{left, right, changed}`;
- `headers`: `[{name, kind, left, right}]` (values as lists, never null),
  and `headersAdded`, `headersRemoved`, `headersChanged`;
- `body`: `{mode, identical, rows, changes, added, removed, changed,
  notices, leftSize, rightSize}`; a row is `{kind, left, right, path}`
  with `left` and `right` `{n, text}` or null, a change `{path, kind,
  left, right, leftType, rightType}`.

Errors: `http.invalid_stored_response` when a Base64 body does not
decode. Nothing is read from disk and nothing is written.

## UI

**Compare…** in the response pane's source bar (next to *Create mock
route…* and *Save as example*) is enabled once there are two responses to
compare: the latest send (when recorded), the earlier responses and the
examples. The dialog (1,100 px wide at most) has a **Left** and a
**Right** select with those, and **Swap**; it opens with the response
shown on the left (else the first other one) and the latest on the right.
Changing a side compares again.

- **Status**: "Status: 200 OK on the left, 404 Not Found on the right",
  or the same status on both sides.
- **Headers**: the counts, and a table (Header, Left, Right) of the names
  that differ; **Show unchanged** adds the others. Several values of a
  name are one per line; a missing side shows a dash.
- **Body**: a summary (counts by JSON path or lines, or "The bodies hold
  the same JSON"), the notices, the JSON changes by path folded under
  "N changes by path", and the side-by-side table: line numbers and text
  of each side. Unchanged runs are folded but for three lines of context
  around each change ("Show 12 unchanged lines" unfolds one run).
- **Not by colour alone**: removed lines are tinted red, added green,
  changed amber, and each has a visible marker (`−`, `+`, `~`, hidden
  from screen readers) and, for screen readers, its kind before the text
  ("Removed:", "Added:", "Changed:"); header rows the same. The tables
  have captions and column headers; the selects are labelled.

## Tests

`internal/core/respdiff/respdiff_test.go`: JSON changes by path (member
and array changes, type changes, odd keys in brackets, duplicate keys,
numbers by value, member order), the rows of a JSON diff (each side reads
as its pretty-printed text, line numbers follow), header names ignoring
case with repeated fields, the line diff (cases and 200 random edits
checked against the longest common subsequence: minimal, and both texts
rebuilt), and every limit and notice. `compare_test.go` goes through the
service (Base64 body, no null lists, an invalid body). In the frontend,
`lib/responseDiff.test.ts` (folding), `CompareResponsesDialog.test.tsx`
(both tables, markers and screen-reader kinds, folding, swapping, an
example side, errors, the accessibility audit),
`ResponseSourceBar.test.tsx` (the button and the default sides) and
`response.pseudo.test.tsx` (every text from the catalogue).

## Not handled yet

- Matching array elements by content or by an id member (arrays are
  compared by index).
- Comparing XML by structure (it is compared as text), or text in other
  charsets (a body that is not UTF-8 is compared as bytes).
- Comparing responses of different requests, or history entries from
  the history panel (NAV-03).
- Word-level highlighting inside a changed line.
