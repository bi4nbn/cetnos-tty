#!/bin/bash
# getty-exit.sh: called by getty@tty1 ExecStopPost after user logs out.
# Wait for tty1 to fully release, then bring the menu back.
sleep 1
/usr/bin/systemctl start linux-tty@tty1.service
