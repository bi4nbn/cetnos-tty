#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Test option 7: menu -> getty (password login) -> shell -> exit -> menu.
# Usage: opt7.py <user> <password>
import fcntl, os, termios, time, subprocess, sys

DEV = '/dev/tty1'
VCS = '/dev/vcs1'
# login credentials are passed as argv: opt7.py <user> <password>
USER = sys.argv[1] if len(sys.argv) > 1 else 'root'
PASS = sys.argv[2] if len(sys.argv) > 2 else 'root'

def inject(data):
    fd = os.open(DEV, os.O_RDWR)
    for ch in data:
        fcntl.ioctl(fd, termios.TIOCSTI, ch.encode())
        time.sleep(0.12)
    os.close(fd)

def screen():
    with open(VCS, 'rb') as f:
        data = f.read()
    lines = [data[i:i+80] for i in range(0, len(data), 80)]
    return b'\n'.join(l.rstrip() for l in lines if l.strip()).decode('utf-8', 'replace')

def active(u):
    return subprocess.call(['systemctl', 'is-active', '--quiet', u]) == 0

def wait(cond, t=12):
    for _ in range(t * 5):
        if cond():
            return True
        time.sleep(0.2)
    return False

inject('7\r')
getty = wait(lambda: active('getty@tty1.service'))
time.sleep(1.5)
s1 = screen()
print('getty active:', getty, '| login prompt:', 'login:' in s1)

inject(USER + '\r')
time.sleep(1.5)
s2 = screen()
print('password prompt:', 'Password:' in s2 or 'assword' in s2)

inject(PASS + '\r')
shell = wait(lambda: active('getty@tty1.service') and '[root@' in screen())
time.sleep(1)
print('shell after login:', '[root@' in screen())

inject('exit\r')
menu = wait(lambda: active('cetnos-tty@tty1.service') and not active('getty@tty1.service'))
time.sleep(1)
print('back to menu:', 'SSCLOUD' in screen(), '| getty inactive:', not active('getty@tty1.service'))
print('OPTION7', 'PASS' if getty and 'assword' in (s2.lower()) and 'SSCLOUD' in screen() else 'FAIL')
