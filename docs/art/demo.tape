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

Hide
Type "kill $DEMO_SERVER; rm -rf $(dirname $LOC_HOME)"
Enter
Sleep 500ms
