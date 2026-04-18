# VHS tape for the ctrl+p file picker demo.
# Records the workflow of fuzzy-finding a file in the sidebar.

Output .demo/file-picker.gif

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 720
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set PlaybackSpeed 1.0
Set WindowBar Colorful
Set WindowBarSize 30
Set Padding 10

Env GITHUB_TOKEN "demo"
Env TERM "xterm-256color"

Hide
Type "cd ~/demo-repo && clear"
Enter
Sleep 300ms
Show

Type "./.bin/ghq"
Sleep 600ms
Enter

# Wait for the diff to load.
Sleep 2.5s

# Open the file picker.
Ctrl+p
Sleep 1.2s

# Type a fuzzy query — the picker scores by characters in order.
Type "fuzz"
Sleep 1.2s

# Select the match.
Enter
Sleep 1.5s

# Open the picker again, search for a different file.
Ctrl+p
Sleep 800ms
Type "post"
Sleep 1s
Enter
Sleep 1.5s

# One more — this time pick by directory name.
Ctrl+p
Sleep 800ms
Type "comp/mod"
Sleep 1.2s
Enter
Sleep 2s

# Quit cleanly.
Ctrl+c
Ctrl+c
Sleep 500ms
