---
description: Try grasp on a sample session (no agent work needed)
allowed-tools: Bash
---
Start the grasp demo in the background and tell the user where to open it.

Run this with the Bash tool, in the background (it keeps running):

"${CLAUDE_PLUGIN_ROOT}/scripts/grasp" demo

It prints the address and opens the browser. Reply with one short line: the address, and that it shows two real Claude Code sessions on a small Go API to review (guess what the new code does, see what it could break, approve or reject). Nothing is kept when it stops.
