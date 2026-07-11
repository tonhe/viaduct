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
#Set Theme "Solarized Dark - Patched"
Set TypingSpeed 60ms
Set CursorBlink false

Require via

Sleep 500ms

Type "via routerjockey.com"
Sleep 500ms
Enter

# Let the TUI render and hops populate
Sleep 8s

# Cycle to Health display mode
Type "d"
Sleep 4s

# Cycle to Latency display mode
Type "d"
Sleep 4s

# Cycle to Variability display mode
Type "d"
Sleep 4s

# Back to Default
Type "d"
Sleep 2s

# Quit cleanly
Type "q"
Sleep 2s
