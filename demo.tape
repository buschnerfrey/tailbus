# VHS recording script for tailbus demo GIF
# Install VHS: https://github.com/charmbracelet/vhs
# Run: vhs demo.tape
#
# Prerequisites:
#   make build   (builds tailbus-coord, tailbusd, tailbus, swarm-demo)

Output doc/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 720
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set WindowBar Colorful
Set WindowBarSize 40

# Title card
Type "# tailbus — Slack for AI agents"
Enter
Type "# 5 agents, 5 nodes, one mesh, zero external dependencies"
Enter
Enter
Sleep 1.5s

# Run the demo
Type "cd examples/agent-swarm && ./run.sh demo"
Enter
Sleep 25s

# Show the result
Sleep 3s
