# TUI design guide

The visual rules for `via`'s terminal UI: which color means what, what each glyph means, how columns are sized and dropped, and how the views are laid out. Anything that changes `internal/tui` follows this document, and a change to what the TUI renders updates this document in the same pull request.

Everything here describes what the code renders today. The rendered output is pinned by golden files under `internal/tui/testdata/`.

## Principles

- **Terminal-native.** Monospace grid, ANSI color, box-drawing characters. Nothing is adapted from a GUI.
- **Dense and aligned.** Network engineers want numbers. Horizontal space goes to addresses and statistics, and every column has a fixed width so rows line up.
- **Color carries meaning.** Green, red, amber and dim each have one job (below). A color used for decoration weakens the ones used for signal.
- **A glyph means the same thing everywhere.** A `·` in the sparkline and a non-zero Loss% describe the same event. `†` always means "this figure comes from direct echoes".
- **Complexity is opt-in.** The default view is an mtr-style table. `d` cycles to more specialised views. Multi-target and column views appear only with more than one target.
- **Live first.** The table redraws every 100 ms. Numbers update in place. Nothing animates.

## Color

Themes are Base16 palettes (`internal/theme`). The TUI never hard-codes a color. Every style is built from the active theme's slots at render time, so a theme change takes effect on the next frame.

| Slot | Used for |
|---|---|
| `Base00` | background of table rows and empty space |
| `Base01` | background of the header bar, separator, summary header, status bar and the settings box |
| `Base02` | selection background: focused summary row, highlighted settings item |
| `Base03` | lowest-emphasis marks: `*` for a silent hop, tree connectors, `... +N more paths`, a rate-limited loss cell, an ASN that did not change, dim sparkline cells |
| `Base04` | dim text: hop numbers, column headers, bar text, `— stable` |
| `Base05` | body text: hostnames, header text, plain numbers |
| `Base06` | text on a selection background, settings titles |
| `Base08` | bad: non-zero loss, `▲ degrading`, the alert bar (bold), error messages |
| `Base09` | amber: the largest hop-to-hop latency delta |
| `Base0A` | an ASN boundary (bold): the first hop in a new AS |
| `Base0B` | good: 0% loss, `▼ improving` |
| `Base0D` | addresses, the hop number of a divergent hop, the focused pane's border and label |

The settings box border is `Base04` on dark themes and `Base03` on light themes.

Loss cells are two-tier: `Base0B` at exactly 0%, `Base08` above it. There is no intermediate tier.

### Sparkline gradient

The sparkline does not use theme accents. Base16 accents are chosen for syntax highlighting and interpolate badly. `Theme.SparkGradient` returns eight fixed stops from green through gold to red, with a darker variant for light themes.

A cell's color is its RTT's deviation above the median of the latency-bearing cells currently shown:

| Deviation above median | Stops |
|---|---|
| at or below median | greenest |
| up to 20% | green |
| 20% to 50% | green to gold |
| 50% to 100% | gold to red |
| over 100% | red |

## Glyphs

| Glyph | Where | Meaning |
|---|---|---|
| `*` | hop row, pane row | no reply from this TTL |
| `...` | Hostname, ASN | lookup still pending |
| `-` | any stat cell | no value (no samples, or not shown for a secondary ECMP node) |
| `~N%†` | Loss% | hop is rate-limited. The figure is the loss of direct echoes to the hop. |
| `├──` `└──` | tree column | ECMP nodes at one TTL. `└──` is the last. |
| `... +N more paths` | under a divergent hop | N nodes not shown for lack of rows |
| `▲ degrading` | Trend | latency rising over the last 10 rounds |
| `▼ improving` | Trend | latency falling |
| `— stable` | Trend | neither |
| `▶` | summary panel | the focused target |
| `►` | settings overlay | the cursor |
| `*name` | summary panel, pane label | the target is marked |
| `…` | addresses, summary names | truncation |
| `─` | under the header bar | separator |
| `╭ ╮ ╰ ╯ ─ │` | column view, settings | pane and box borders |

Only the light box-drawing set is used. Heavy box-drawing renders at inconsistent widths in many terminal fonts, and a border is a long run of horizontals, so the drift adds up and misaligns the next pane. Focus is shown with color and a bold label.

### Sparkline cells

One cell per round, most recent on the right, left-padded with spaces. The glyph's solidity says how much the tracer learned about the hop that round.

| Glyph | Sample kind | Meaning | Color |
|---|---|---|---|
| `█` | TTL | a TTL-limited probe was answered. RTT is the round's average. | gradient |
| `▓` | Echo | TTL-limited probes went unanswered, a direct echo was answered. RTT is the echo's. | gradient |
| `░` | Held | hop is known rate-limited and was not echoed this round. Reachable, no reading. | `Base03` |
| `·` | Drop | probes were sent and nothing answered | `Base03` |

A drop is written only when no tier reached the hop, so the sparkline's dots agree with the loss column for a rate-limited hop. The sparkline column is 14 cells wide. Each node keeps its last 28 samples.

## Numbers and text

- **Durations** are milliseconds with one decimal and no unit (`12.4`). Under one millisecond they are whole microseconds with the unit (`850µs`). Zero is `-`.
- **Delta** is signed (`+3.2`, `-0.4`). The first responding hop shows its own average.
- **Loss%** has one decimal (`0.0%`, `12.5%`). In column panes it is a whole number (`0%`, `13%`).
- **StDev** has one decimal. `-` when zero.
- **Rnd** is a count of rounds.
- **ASN** is `AS13335`, or `AS13335 (ORG)` when there is room.
- **Addresses** are shown in full when they fit. One that doesn't fit is cut in the middle, `2001:db8…55:6661`, because hops in one network share a prefix and differ at the end.
- **Hostnames** in the hop table are cut at the column width with no ellipsis.
- **Target names** in the summary panel and pane labels are cut with a trailing `…`.
- Every cell is left-aligned in the hop table. In column panes `#`, `Avg` and `Loss` are right-aligned.

**Pad first, then style.** Pad the plain text to the column width and style the result. `fmt` pads by byte length, so padding an already-styled string miscounts its width. The mistake is invisible in tests that strip color and garbles a real terminal. Use `padCell`.

## Screen layout

Single target, top to bottom:

```
via v0.3.0 (build)   Target: example.com (192.0.2.1)    Proto: UDP/ECMP    Probes: 420
────────────────────────────────────────────────────────────────────────────────────────
#        IP              Hostname        ASN      Loss%    Rnd   Avg     Last    Spark
1        192.0.2.1       gw.example.net  AS64496  0.0%     10    0.5     0.4     ██████████
2        198.51.100.5    ...             AS64496  ~0%†     10    8.2     8.0     █▓░░▓█░▓░█
3   ├──  203.0.113.10    a.example.net   AS64511  0.0%     10    12.4    12.1    ██████████
    └──  203.0.113.20    b.example.net            -        -     13.2    13.0    ██████████
4        *
5        203.0.113.99    example.com     AS64511  10.0%    10    12.1    11.9    ████·█████

! Sustained loss to destination: 10.0% (hop 5 affected, AS64511)
Tracing... 5 hops    Elapsed: 10s    DNS: on    6 flows    j/k:scroll p:pause r:reset [d] Health q:quit
```

1. **Header bar.** `via <version>`, target name and address, protocol, reply count. The protocol shows `/ECMP` when more than one flow is probed and `Auto → UDP` (or `ICMP`) in auto mode. Outside the default view it ends with `View: <name>`.
2. **Separator.**
3. **Summary panel**, with more than one target (below).
4. **Column header.**
5. **Hop rows**, one per TTL up to the destination. Rows that don't fit scroll.
6. **Alert bar**, only while an alert is active. With more than one target it is prefixed with the target name.
7. **Status bar.** Trace state (`Tracing... N hops` or `PAUSED`), elapsed time, DNS on or off, flow count, a notice or transient message, and key hints. N is the destination's distance once it has answered and the highest TTL seen until then. The `[d]` hint names the view the next press switches to. When the bar does not fit, segments are left out in this order: the Probe Detail note, DNS, flows, elapsed, then the key hints one at a time, least used first (`r:reset`, `e:export`, `p:pause`, `space:mark`, `j/k:scroll`, `c:columns`, `[d]`, `↑/↓:target`), then `[scrolled]` and last the notice. The hop count and `q:quit` are always shown. The order is the `drop` constants in `internal/tui/fit.go`.

The header, separator and status bar use the bar background. Everything between uses the row background, padded to the full terminal width so no terminal default shows through.

### Hop rows

- A TTL with no reply is the hop number and `*`.
- A hop with one responder is one row.
- A divergent hop (more than one responder) is one row per node, most replies first. The hop number appears on the first row only, in `Base0D`. Loss%, Rnd, ASN, Delta and Trend appear on the first row only. The other rows show `-` or blank there, because a TTL-limited probe is addressed to a hop distance and its loss can't be attributed to one node.
- How many nodes a divergent hop shows depends on height. Every TTL gets one row first. Spare rows are handed to divergent hops from the top down. Nodes that don't get a row are summarised as `... +N more paths`.
- An ASN cell is bold `Base0A` when its AS number differs from the previous responding hop's, and `Base03` otherwise.
- The largest positive Delta in the table is amber.

### Column widths and priority

Columns are joined by one space.

| Column | Width |
|---|---|
| `#` | 3 |
| tree | 4, present when more than one flow is probed |
| `IP` | longest address shown, between 15 and 39 |
| `Hostname` | 15, grows in steps of 10 |
| `ASN` | longest label shown, at least 7 |
| `Loss%` | 8 |
| `Rnd` | 5 |
| `Avg` `Best` `Wrst` `StDev` `Last` `Delta` `GMean` `Jttr` `Javg` | 7 each |
| `Spark` | 14 |
| `Trend` | 9 |

`#`, `IP`, `Loss%`, `Avg` and `Spark` are reserved first. The remaining width is spent in a priority order that depends on the view:

| View | Added in this order |
|---|---|
| Default | Hostname, Rnd, Last, ASN, Best, Wrst, StDev, wider Hostname (three steps), ASN with org name |
| Health | Hostname, Delta, Trend, ASN, wider Hostname (two steps), ASN with org name |
| Latency | Hostname, Delta, Best, Wrst, ASN, Last, GMean, wider Hostname (two steps), ASN with org name |
| Variability | Hostname, StDev, Jttr, Javg, Trend, ASN, wider Hostname (two steps), ASN with org name |

A terminal narrower than 40 columns is laid out as 40.

If the result is still too wide (a 39-character IPv6 address is the usual cause), the layout gives things up in this order until it fits: Hostname down to 10, ASN down to 7, then whole columns in the order Trend, Javg, Jttr, GMean, Delta, StDev, Wrst, Best, Last, Rnd, ASN, Hostname, Spark. After that the tree column goes if no hop is divergent, and last the IP column narrows toward 15 with addresses cut in the middle.

### Probe Detail view

The fifth view replaces the hop table with per-probe-type counts, one row per node:

```
#   IP                ICMP-TTL    UDP-TTL     Echo        Rnd
1   192.0.2.1         10 (0%)     10 (0%)     9 (0%)      10
2   198.51.100.5      3 (70%)     2 (80%)     3/5         10
```

- Each probe cell is `received (loss%)`, 11 wide, green at 0% and red above.
- The middle column is `UDP-TTL` or `TCP-TTL`. In ICMP mode it shows `—`, because the ICMP TTL probe is the primary probe.
- The Echo cell shows `N/5` in dim text until 5 echoes have been sent to the node. "Not enough samples yet" must not look like "0% loss".
- The IP column is at least 17 wide.

## Multi-target

### Summary panel

With more than one target and column view off, a panel sits between the separator and the hop table:

```
  TARGET                    HOPS    LOSS       AVG  WRST-HOP         ALERT
▶ example.com                 12    0.0%    12.1ms  203.0.113.10     —
  *example.net                 9    2.5%    30.4ms  198.51.100.5     ! Sustained loss to destination: 6.0%
  example.org               error: target has no IPv6 (AAAA) record
```

- The header row uses the bar background.
- The focused row starts with `▶` and is drawn `Base06` on `Base02`. The hop table below belongs to the focused target.
- LOSS and AVG are the last responding hop's. WRST-HOP is the address of the hop with the largest positive latency delta, the same hop the table marks in amber.
- A marked target's name is prefixed with `*`.
- A target that failed at setup keeps its row and shows the error. Focusing it replaces the hop table with the error message.
- Names longer than 24 characters and addresses longer than 16 are cut with `…`.

### Column view

`c` switches to one bordered pane per target, side by side:

```
╭ example.com ───────────────────╮ ╭ example.net ───────────────────╮
│  # IP                Avg  Loss │ │  # IP                Avg  Loss │
│  1 192.0.2.1         0.5    0% │ │  1 192.0.2.1         0.5    0% │
│  2 198.51.100.5      8.2    0% │ │  2 198.51.100.77     9.1    0% │
│  3 203.0.113.10     12.4    0% │ │  3 *                           │
│  4 203.0.113.99     12.1   10% │ │                                │
╰────────────────────────────────╯ ╰────────────────────────────────╯
```

- Panes divide the terminal width equally, with a one-column gap so borders never touch. Two targets get half the width each. Few targets get wide panes.
- A pane is never narrower than 34 columns and there are never more than 6. When there are more targets than fit, the first ones are shown and a line reads `+N more — space to mark which show`. The focused target is always among the panes shown.
- If any target is marked, only marked targets are shown. Marking is how the user chooses panes.
- Targets that failed at setup get no pane.
- The focused pane has a `Base0D` border and a bold `Base0D` label. Other panes have a dim border.
- The label sits one space after the corner, so it lines up with the pane's content column.
- Pane columns fill the pane by priority: `#` (2) and `IP` (at least 9) always, then `Avg` (6), then `Loss` (5), then a wider `IP` (up to 15, or up to the longest address the pane shows when that is longer), then `Host` with whatever is left if that is at least 8.
- All panes share one row grid. A pane's rows past its own destination are blank.

## Settings overlay

`Esc` opens a rounded box centered on screen, 50 columns wide or the terminal width minus 4 if that is smaller. The box background is `Base01` and the screen behind it is filled with `Base00`.

The main menu lists Theme, Probe Settings, Display & Enrichment, Alerting, then Save & Exit and Cancel under a divider. The cursor row is prefixed with `►` and drawn bold `Base06` on `Base02`. Submenus show a `›` hint. A dim footer line gives the keys.

The theme list shows 12 themes at a time and applies each theme as the cursor moves over it, so the whole screen previews it. `Esc` from the list restores the previous theme. Leaving the overlay with unsaved changes asks for confirmation. Saving writes the config file.

## Themes

61 themes ship in `internal/theme/themes.go`. Each is a name, a slug, a light flag and the sixteen Base16 colors. Solarized Dark is the default.

Resolution order: `--theme <slug>`, then `theme` in the config file, then `solarized-dark`. An unknown slug on the command line is an error that lists the valid slugs. An unknown slug in the config file falls back to the default with a warning.

A new theme needs only its sixteen colors. If a theme needs a special case in the TUI, the TUI is using the wrong slot.

## Keys

| Key | Action |
|---|---|
| `q`, `Ctrl-C` | quit and print the final summary |
| `Esc` | open settings |
| `p` | pause or resume probing (all targets) |
| `r` | reset statistics (all targets) |
| `d` | next view: Default, Health, Latency, Variability, Probe Detail |
| `e` | export to a file in the current directory |
| `j` / `k` | scroll the hop table down / up |
| `g` / `G` | jump to top / back to bottom (follow mode) |
| `↓` / `↑` | one target: scroll. Several targets: move focus. |
| `Tab` | several targets: focus the next target, wrapping |
| `Space` | several targets: mark or unmark the focused target |
| `c` | several targets: toggle column view |

Scrolling up leaves follow mode and the status bar shows `[scrolled]`. `G` returns to it.

## Adding to the UI

- Reuse an existing color role and glyph before adding one. A new one gets a row in the tables above.
- Give a new column a fixed width, a place in each view's priority order and a place in the drop order.
- Check the result at 80 columns, at 120, and with a 39-character IPv6 address.
- Add or update golden files (`make golden-update`) and read the diff before committing.
