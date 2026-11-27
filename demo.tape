# demo.tape — VHS recording for via (Viaduct)
#
# Install VHS: brew install vhs
# Record:      vhs demo.tape

Output img/demo.gif
Output img/demo.webm

# Terminal appearance
Set FontFamily "JetBrains Mono"
Set FontSize 14
Set LetterSpacing 0
Set LineHeight 1.2
Set Width 1400
Set Height 750
Set Padding 15
Set WindowBar "Colorful"
Set Theme "Solarized Dark Higher Contrast"
Set TypingSpeed 60ms
Set CursorBlink false

Require via

Sleep 500ms

# Two targets in one session
Type "via routerjockey.com one.one.one.one"
Sleep 500ms
Enter

# Let the TUI render and hops populate
Sleep 9s

# Move focus to the second target and back
Tab
Sleep 3s
Tab
Sleep 2s

# Column view: the targets side by side
Type "c"
Sleep 5s
Type "c"
Sleep 2s

# Cycle the display modes: Health, Latency, Variability, Probe Detail
Type "d"
Sleep 3s
Type "d"
Sleep 3s
Type "d"
Sleep 3s
Type "d"
Sleep 4s

# Back to Default
Type "d"
Sleep 2s

# Quit cleanly
Type "q"
Sleep 2s
