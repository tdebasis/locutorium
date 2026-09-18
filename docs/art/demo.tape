# demo.tape — two agents, one conversation. Rendered by vhs: a hidden preamble builds a
# scratch house on a random port, so every line on screen is the tool's real output.
Output demo.gif
Set Shell bash
Set FontSize 16
Set Width 1000
Set Height 620
Set Padding 24
Set Theme "Catppuccin Latte"
Set TypingSpeed 40ms

Hide
Type "source ./prep.sh"
Enter
Wait+Line@60s /^> ?$/
Sleep 500ms
Show

Type `LOC_IDENTITY=house.ada loc send house.bob "the build is green; the tag is yours"`
Sleep 500ms
Enter
Sleep 1500ms
Type `LOC_IDENTITY=house.ada loc publish standup "@house.bob please look at the restore proof"`
Sleep 500ms
Enter
Sleep 1500ms
Type "LOC_IDENTITY=house.bob loc read"
Sleep 500ms
Enter
Wait+Screen@20s /── topics ──/
Sleep 2s
Type "LOC_IDENTITY=house.bob loc topics"
Sleep 500ms
Enter
Wait+Screen@20s /in window/
Sleep 1500ms
Type "LOC_IDENTITY=house.bob loc status"
Sleep 500ms
Enter
Wait+Screen@20s /house.bob    unread/
Sleep 3s

# THE TEARDOWN STOPS THE DAEMON BEFORE IT REMOVES THE HOME, AND ONLY IF THE STOP
# WORKED. The home holds the config, and the config is the only thing keeping this
# daemon on its own port. A daemon that outlives its home falls back to the default
# broker address and sweeps it with an empty ledger, deleting every queue it finds.
# That happened: the previous teardown was `kill $DEMO_SERVER; rm -rf …`, the kill
# did not land, and the survivor reaped a live deployment every five minutes.
# `loc stop` waits for the port to close and fails if it does not; `&&` keeps the
# home in place on any failure, so a survivor keeps its own config.
Hide
Type "loc stop && rm -rf $(dirname $LOC_HOME)"
Enter
Sleep 5s
