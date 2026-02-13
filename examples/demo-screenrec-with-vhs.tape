# script for https://github.com/charmbracelet/vhs to create a screen recording
#
# Install:
# go install github.com/charmbracelet/vhs@latest
# sudo apt install ttyd
#
# cd echoserver
# make run
#
# cd echoclient
# PS1="\[\033[01;32m\]>\[\033[00m\] " vhs examples/demo-screenrec-with-vhs.tape
#

Output examples/demo.gif

Set FontSize 12
Set FontFamily "DejaVu Sans Mono"

Set Width 1050
Set Height 600
Set Padding 10

Type "./echoclient get -concurrency 100 -duration 15s -url http://localhost:8080
Enter
Sleep 17s
