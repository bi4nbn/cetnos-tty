#!/usr/bin/env python
# -*- coding: utf-8 -*-
# End-to-end test of the hidden 666 passwordless root shell:
# menu --666--> root shell --exit--> menu, repeated N times.
import fcntl, os, termios, time, sys, subprocess

TTY = 'tty1'
DEV = '/dev/tty1'
VCS = '/dev/vcs1'

def inject(data):
    fd = os.open(DEV, os.O_RDWR)
    for ch in data:
        c = ch if isinstance(ch, str) else chr(ch)
        fcntl.ioctl(fd, termios.TIOCSTI, c.encode() if isinstance(c, str) else c)
        time.sleep(0.12)
    os.close(fd)

def screen():
    with open(VCS, 'rb') as f:
        data = f.read()
    lines = [data[i:i+80] for i in range(0, len(data), 80)]
    return b'\n'.join(l.rstrip() for l in lines if l.strip()).decode('utf-8', 'replace')

def active(unit):
    return subprocess.call(['systemctl', 'is-active', '--quiet', unit]) == 0

def wait_for(cond, timeout=12):
    for _ in range(timeout * 5):
        if cond():
            return True
        time.sleep(0.2)
    return False

results = []
for i in range(3):
    # enter shell
    inject('666\r')
    ok_shell = wait_for(lambda: active('cetnos-root@tty1.service'))
    time.sleep(1)
    has_prompt = '[root@' in screen()
    # exit shell
    inject('exit\r')
    ok_menu = wait_for(lambda: active('cetnos-tty@tty1.service') and not active('cetnos-root@tty1.service'))
    time.sleep(1)
    has_menu = 'SSCLOUD' in screen()
    ok = ok_shell and has_prompt and ok_menu and has_menu
    results.append(ok)
    print('round %d: shell=%s prompt=%s menu=%s menu_screen=%s -> %s' % (
        i + 1, ok_shell, has_prompt, ok_menu, has_menu, 'PASS' if ok else 'FAIL'))
    time.sleep(1)

print('ALL PASS' if all(results) else 'SOME FAILED')
