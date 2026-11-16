---
description: Open the grasp review page for this project
allowed-tools: Bash
---
Start the grasp review page for the current project in the background and tell the user where to open it.

Run this with the Bash tool, in the background (it keeps running):

"${CLAUDE_PLUGIN_ROOT}/scripts/grasp" serve

It prints the address, http://127.0.0.1:7080. If that port is busy, run it again with `-addr 127.0.0.1:7081`. Reply with one short line: the address, and that they can review the work there decision by decision. If it fails because there are no sessions yet, say that grasp fills up as you work and to try again after some edits.
